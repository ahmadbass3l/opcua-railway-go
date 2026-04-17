# Module Reference — opcua-railway-go

> **Audience:** Developers maintaining or extending the Go service.
> Every package is described: its purpose, exported API, and key design choices.

---

## File layout

```
opcua-railway-go/
├── main.go                  Entry point — wiring, server, graceful shutdown
├── config/
│   └── config.go            Env var loading
├── opcua/
│   └── client.go            gopcua subscription client
├── sse/
│   └── broker.go            SSE broker — fan-out to connected HTTP clients
├── db/
│   └── writer.go            TimescaleDB pool, write, and query helpers
│   └── init.sql             Schema: hypertable + continuous aggregate
├── api/
│   └── handlers.go          HTTP handlers for all endpoints
├── go.mod / go.sum          Module dependencies
├── Dockerfile               Multi-stage container build
├── docker-compose.yml       Full stack: TimescaleDB + this service + Python service
└── docs/                    This documentation
```

---

## `config/config.go` — Configuration

**Purpose:** Single source of truth for runtime configuration.
Reads from OS environment variables with typed defaults.

```go
type Config struct {
    OpcuaEndpoint   string    // opc.tcp://hardware:4840
    OpcuaNodeIDs    []string  // parsed from comma-separated env var
    OpcuaIntervalMs uint32    // publishing interval in ms
    DBDSN           string    // postgresql://...
    Port            string    // "8080"
}

func Load() Config
```

**Design choice:** No external library (no Viper, no godotenv).
Pure `os.Getenv` with explicit defaults — zero dependencies, zero ambiguity.
For `.env` file support in local development, pass env vars directly:
```bash
export $(cat .env | xargs) && go run .
```

---

## `sse/broker.go` — SSE Broker

**Purpose:** Decouples the OPC UA ingest goroutine from HTTP response handlers.
Implements a publish/subscribe fan-out using buffered Go channels.

### `Reading` struct

```go
type Reading struct {
    SensorID string    `json:"sensor_id"`
    NodeID   string    `json:"node_id"`
    Value    float64   `json:"value"`
    Unit     string    `json:"unit"`
    Quality  int       `json:"quality"`
    Time     time.Time `json:"time"`
}

func (r Reading) ToSSEFrame() string
// Returns "event: reading\ndata: {...JSON...}\n\n"
```

### `Broker` struct

```go
type Broker struct {
    mu      sync.RWMutex
    clients map[string]chan Reading  // clientID → buffered channel (cap 256)
}

func NewBroker() *Broker

func (b *Broker) Subscribe() (string, <-chan Reading)
// Register a new SSE client. Returns (id, receive-only channel).

func (b *Broker) Unsubscribe(id string)
// Remove client, close its channel.

func (b *Broker) Publish(r Reading)
// Fan-out to all client channels.
// Slow clients: drain oldest, insert newest (non-blocking).

func (b *Broker) ClientCount() int
```

**Concurrency:** `Publish` holds a read lock (`RLock`) so multiple goroutines
can call it concurrently without blocking. `Subscribe`/`Unsubscribe` take a
write lock. In practice, all calls come from two goroutines (OPC UA handler +
HTTP handler) so contention is minimal.

### Slow consumer handling

```go
select {
case ch <- r:          // fast path: channel has room
default:
    <-ch               // drain one (oldest)
    ch <- r            // insert newest
}
```

This is a non-blocking operation — the OPC UA goroutine is never blocked by a
slow HTTP client.

---

## `db/writer.go` — Database Layer

**Purpose:** All TimescaleDB interaction via `pgx/v5`.

### Initialisation

```go
var Pool *pgxpool.Pool

func Init(ctx context.Context, dsn string) error
// Creates pgxpool, pings DB. Assigns Pool.

func Close()
// Shuts down the pool.
```

### Write

```go
func InsertReading(ctx context.Context, r sse.Reading) error
// INSERT INTO sensor_readings ...
```

Called from `main.go` per reading with a 5-second timeout context.

### Queries

```go
type QueryReadingsParams struct {
    SensorID string
    From, To time.Time
    Limit    int
}

func QueryReadings(ctx context.Context, p QueryReadingsParams) ([]ReadingRow, error)
// Raw readings from sensor_readings hypertable

func QueryAggregate(ctx context.Context, sensorID string, from, to time.Time) ([]AggRow, error)
// From readings_1min continuous aggregate view

func ListSensors(ctx context.Context) ([]string, error)
// DISTINCT sensor_id values

func HealthCheck(ctx context.Context) bool
// Pool.Ping
```

**Design choice:** `pgx/v5` native driver (no GORM, no sqlx).
Explicit SQL gives full control over query plans. `pgxpool` manages a pool of
persistent connections — no connection overhead per request.

---

## `opcua/client.go` — OPC UA Client

**Purpose:** Connects to the hardware OPC UA server, subscribes to sensor nodes,
calls the provided `Handler` function for each `DataChangeNotification`.

### Handler type

```go
type Handler func(r sse.Reading)
```

### Run function

```go
func Run(ctx context.Context, cfg config.Config, h Handler)
// Blocking. Runs in a goroutine from main.go.
// Reconnects automatically on error with exponential back-off (2s → 60s).
```

### `runOnce` — single connection attempt

```go
func runOnce(ctx context.Context, cfg config.Config, h Handler) error
```

1. `opcua.NewClient(endpoint, SecurityMode(None))` — creates client
2. `c.Connect(ctx)` — TCP + OPC UA handshake
3. `c.Subscribe(ctx, params, notifyCh)` — create subscription
4. Loop over `cfg.OpcuaNodeIDs` → `sub.Monitor(ctx, ...)` — add MonitoredItems
5. Select loop: `notifyCh` → parse `DataChangeNotification` → cast value → call `h`

### Client handle mapping

```go
handleToNode := map[uint32]string{}
// handle 1 → "ns=2;i=1001"
// handle 2 → "ns=2;i=1002"
```

The OPC UA `ClientHandle` is a `uint32` assigned by the client when creating
each MonitoredItem. On receiving a notification, we reverse-lookup the NodeId
string from this map.

### `sensorMeta` map

```go
var sensorMeta = map[string][2]string{
    "ns=2;i=1001": {"rail_temp_1",       "°C"},
    "ns=2;i=1002": {"wheel_vibration_1", "mm/s"},
    "ns=2;i=1003": {"brake_pressure_1",  "bar"},
}
```

Extend this when adding new sensor nodes. See `01-opc-ua-explained.md` for
how to discover NodeIds from the hardware address space.

### `toFloat64` helper

```go
func toFloat64(v interface{}) (float64, bool)
```

OPC UA `Value.Value()` returns `interface{}`. This function handles all
common numeric types (`float32`, `float64`, `int16/32/64`, `uint16/32/64`).
Non-numeric values are discarded with a warning log.

---

## `api/handlers.go` — HTTP Handlers

**Purpose:** All HTTP request handling. Stateless — receives broker and uses
the global DB pool.

### Handler factories (where state is needed)

```go
func HandleStream(broker *sse.Broker) http.HandlerFunc
func HandleHealth(broker *sse.Broker) http.HandlerFunc
```

The broker is injected via closure — handlers are pure functions of their input.

### Plain `HandlerFunc` values (stateless)

```go
func HandleReadings(w http.ResponseWriter, r *http.Request)
func HandleAggregate(w http.ResponseWriter, r *http.Request)
func HandleSensors(w http.ResponseWriter, r *http.Request)
```

These read from the global `db.Pool` directly.

### SSE handler mechanics

```go
flusher, ok := w.(http.Flusher)
// Asserts http.Flusher interface — available in net/http's ResponseWriter.
// Required to flush each SSE frame to the client immediately.

id, ch := broker.Subscribe()
defer broker.Unsubscribe(id)

for {
    select {
    case <-r.Context().Done():   // client disconnected
        return
    case reading := <-ch:        // new reading from OPC UA
        fmt.Fprint(w, reading.ToSSEFrame())
        flusher.Flush()          // push bytes to TCP send buffer immediately
    case <-keepAlive.C:          // 15-second keep-alive
        fmt.Fprint(w, ": keep-alive\n\n")
        flusher.Flush()
    }
}
```

**`http.WriteTimeout` is set to `0`** in `main.go` so long-lived SSE connections
are not killed by the HTTP server's write deadline.

---

## `main.go` — Entry Point and Wiring

**Purpose:** Compose all packages, configure the HTTP server, handle OS signals.

```go
cfg := config.Load()
broker := sse.NewBroker()

db.Init(ctx, cfg.DBDSN)
defer db.Close()

go opcuaclient.Run(ctx, cfg, func(r sse.Reading) {
    broker.Publish(r)
    db.InsertReading(dbCtx, r)
})

srv := &http.Server{
    WriteTimeout: 0,     // no timeout for SSE streams
    ...
}
```

**Graceful shutdown sequence:**
1. `SIGINT` / `SIGTERM` received → `cancel()` context
2. OPC UA goroutine exits (context cancelled)
3. `srv.Shutdown(10s timeout)` — waits for in-flight requests to complete
4. `db.Close()` — drains pool
5. Process exits cleanly

---

## Dependency graph

```
main.go
  ├── config/config.go       (no imports from this module)
  ├── sse/broker.go          (no imports from this module)
  ├── db/writer.go
  │     └── sse/broker.go    (sse.Reading type only)
  ├── opcua/client.go
  │     ├── config/config.go
  │     └── sse/broker.go
  └── api/handlers.go
        ├── db/writer.go
        └── sse/broker.go
```

No circular dependencies. `config` and `sse` are leaf packages. `db` imports
`sse` only for the `Reading` type.
