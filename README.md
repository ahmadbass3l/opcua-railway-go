# opcua-railway-go

Railway sensor telemetry microservice — **Go** implementation.

Connects to an OPC UA server on railway hardware, subscribes to sensor nodes, streams live data to browsers via **Server-Sent Events**, and persists readings to **TimescaleDB**.

> See the companion [Python implementation](https://github.com/ahmadbass3l/opcua-railway-python) for an identical API in Python.

---

## Stack

| Layer | Library |
|---|---|
| OPC UA client | [`gopcua/opcua`](https://github.com/gopcua/opcua) |
| HTTP framework | `net/http` (stdlib) |
| SSE | `http.Flusher` + per-client channel |
| DB driver | `pgx/v5` (pgxpool) |
| Database | TimescaleDB (PostgreSQL) |

---

## Endpoints

| Method | Path | Description |
|---|---|---|
| `GET` | `/stream` | SSE live feed — `event: reading` per sensor change |
| `GET` | `/readings?sensor_id=&from=&to=&limit=` | Historical raw readings |
| `GET` | `/readings/aggregate?sensor_id=&bucket=1min&from=&to=` | 1-min pre-aggregated averages |
| `GET` | `/sensors` | List all known sensor IDs |
| `GET` | `/health` | OPC UA + DB health probe |

### SSE event format

```
event: reading
data: {"sensor_id":"rail_temp_1","node_id":"ns=2;i=1001","value":42.3,"unit":"°C","quality":0,"time":"2026-04-17T19:00:00Z"}

```

---

## Quick start

### Docker Compose (recommended)

```bash
git clone https://github.com/ahmadbass3l/opcua-railway-go.git
cd opcua-railway-go
cp .env.example .env          # edit OPCUA_ENDPOINT and OPCUA_NODE_IDS
docker compose up
```

### Run locally

```bash
export OPCUA_ENDPOINT=opc.tcp://192.168.1.100:4840
export OPCUA_NODE_IDS="ns=2;i=1001,ns=2;i=1002"
export DB_DSN=postgresql://railway:railway@localhost:5432/railway
go run .
```

---

## Configuration

| Variable | Default | Description |
|---|---|---|
| `OPCUA_ENDPOINT` | `opc.tcp://localhost:4840` | OPC UA server on the hardware |
| `OPCUA_NODE_IDS` | `ns=2;i=1001,...` | Comma-separated NodeIds to subscribe to |
| `OPCUA_INTERVAL_MS` | `500` | Subscription publishing interval (ms) |
| `DB_DSN` | `postgresql://railway:railway@localhost:5432/railway` | TimescaleDB connection string |
| `PORT` | `8080` | HTTP server port |

---

## File layout

```
opcua-railway-go/
├── main.go               # Wiring: config, DB, broker, OPC UA client, HTTP server
├── config/
│   └── config.go         # Env var loading
├── opcua/
│   └── client.go         # gopcua connect + Subscribe + DataChangeNotification loop
├── sse/
│   └── broker.go         # Broker: fan-out chan Reading per connected client
├── db/
│   └── writer.go         # pgxpool, InsertReading, QueryReadings, QueryAggregate
├── api/
│   └── handlers.go       # HTTP handlers for all endpoints
├── go.mod
├── Dockerfile
├── docker-compose.yml
└── .github/
    └── workflows/
        └── ci.yml
```

---

## How it works

1. `main.go` initialises the pgxpool, SSE broker, OPC UA client, and HTTP server.
2. `opcua.Run()` connects to `opc.tcp://hardware:4840`, creates a Subscription, and registers one MonitoredItem per NodeId. It runs in a goroutine and reconnects on error with exponential back-off.
3. Each `DataChangeNotification` calls the handler in `main.go`, which:
   - Calls `broker.Publish(reading)` → fans out to all SSE client channels
   - Calls `db.InsertReading(ctx, reading)` → writes to TimescaleDB
4. `GET /stream` acquires the `http.Flusher`, subscribes to the broker, and loops: receive from channel → write SSE frame → `Flush()`.
5. Slow SSE clients use a drop-oldest policy (buffered channel, 256 capacity).

---

## Database schema

See [`db/init.sql`](https://github.com/ahmadbass3l/opcua-railway-go/blob/main/db/init.sql) — creates the `sensor_readings` hypertable and `readings_1min` continuous aggregate.

---

## License

MIT
