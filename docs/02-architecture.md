# Architecture — Railway OPC UA Microservice (Go)

> **Audience:** Developers (Ahmad, Andy, and contributors).
> This document explains the Go-specific architecture, concurrency model,
> and design decisions. For shared decisions (why OPC UA, why SSE, why TimescaleDB)
> see the [Python service architecture doc](https://github.com/ahmadbass3l/opcua-railway-python/blob/main/docs/02-architecture.md).

---

## System overview

```
┌─────────────────────────────────────────────────────────────────────────┐
│                        Railway Field Hardware                           │
│                    OPC UA Server  opc.tcp://hardware:4840               │
└──────────────────────────────────┬──────────────────────────────────────┘
                                   │  OPC UA TCP Binary
                                   │  Subscription + DataChangeNotification
                                   ▼
┌──────────────────────────────────────────────────────────────────────────┐
│                   opcua-railway-go (this service)                        │
│                                                                          │
│  Goroutine: opcua.Run()                                                  │
│  ┌────────────────────────────────────────────────────────────────────┐  │
│  │  gopcua Client                                                     │  │
│  │  Connect → Subscribe → Monitor(NodeIds) → notifyCh loop           │  │
│  │                    │                                               │  │
│  │                    ▼ on DataChangeNotification                     │  │
│  │              Handler func(sse.Reading)                             │  │
│  │                ├── broker.Publish(r)  ──────────────────────────┐ │  │
│  │                └── db.InsertReading(ctx, r)                     │ │  │
│  └────────────────────────────────────────────────────────────────-─┘ │  │
│                                                                        │  │
│  sse.Broker (sync.RWMutex + map[string]chan Reading)                   │  │
│  ┌──────────────────────────────────────────────────────────────────┐ │  │
│  │  client-a: chan Reading (cap 256) ◀─────────────────────────────-┘ │  │
│  │  client-b: chan Reading (cap 256)                                   │  │
│  │  client-c: chan Reading (cap 256)                                   │  │
│  └──────────────────────────────────────────────────────────────────-┘  │
│                    │                                                      │
│  Goroutines: net/http (one per connection)                               │
│  ┌──────────────────────────────────────────────────────────────────┐    │
│  │  GET /stream                                                      │    │
│  │    broker.Subscribe() → ch                                        │    │
│  │    loop: <-ch → flusher.Flush()                                   │    │
│  │                                                                   │    │
│  │  GET /readings          → db.QueryReadings()                      │    │
│  │  GET /readings/aggregate → db.QueryAggregate()                    │    │
│  │  GET /sensors           → db.ListSensors()                        │    │
│  │  GET /health            → db.HealthCheck()                        │    │
│  └──────────────────────────────────────────────────────────────────┘    │
│                                                                           │
│  db.Pool (*pgxpool.Pool) ──────────────────────────────────────────────▶ │
└──────────────────────────────────────────────────────────────────────────┘
                                   │  HTTP / SSE
              ┌────────────────────┼────────────────────┐
              ▼                    ▼                    ▼
    ┌──────────────────┐  ┌──────────────────┐  ┌──────────────────┐
    │  Browser / SPA   │  │  Grafana          │  │  Other services  │
    └──────────────────┘  └──────────────────┘  └──────────────────┘
```

---

## Design decisions

### Why Go for this service?

| Concern | Go advantage |
|---|---|
| Concurrency | Goroutines are cheap (few KB stack). Each SSE connection is a goroutine — 10,000 concurrent streams with minimal memory. |
| Performance | Compiled binary — no interpreter overhead. TimescaleDB writes and SSE fan-out are CPU-bound only at very high rates. |
| Deployment | Single static binary, `~10 MB` Docker image using `distroless/static`. No runtime to install. |
| Standard library | `net/http` + `http.Flusher` handles SSE natively — no framework needed. |
| Memory safety | Bounds-checked, GC-managed — no segfaults in the ingest path. |

### Why `net/http` and not Gin/Echo/Fiber?

The API surface is small (5 endpoints). A full framework would add:
- External dependency with its own update cycle
- Middleware chains harder to reason about for SSE
- Router abstractions that obscure `http.Flusher` usage

`net/http` handles this workload with zero overhead and no surprises.

### Goroutine model

| Goroutine | Count | Lifecycle |
|---|---|---|
| `opcua.Run()` | 1 | Lives for the process lifetime; reconnects internally |
| `net/http` connection handler | 1 per HTTP request | Lives for request duration |
| `net/http` SSE handler | 1 per SSE client | Lives until client disconnects |
| `pgxpool` background workers | 2–10 (pool min/max) | Managed by pgxpool |

At 10 concurrent SSE clients + 1 OPC UA goroutine + pool workers: ~15–25 goroutines.
At 1,000 SSE clients: ~1,015 goroutines, ~50–100 MB RAM. Perfectly viable.

---

## Concurrency and synchronisation

### Between OPC UA goroutine and SSE handlers

The `broker.Publish()` call is the only cross-goroutine communication:

```
opcua goroutine → broker.Publish(r)
                      RLock
                      for each client: ch <- r  (non-blocking)
                      RUnlock

SSE goroutine  ← <-ch
```

- No shared mutable state except the `broker.clients` map
- `sync.RWMutex`: multiple concurrent readers (Publish), exclusive writer (Subscribe/Unsubscribe)
- Channel sends in Publish are non-blocking — no risk of goroutine leak

### Between OPC UA goroutine and DB writer

```
opcua goroutine → db.InsertReading(ctx, r)
```

`pgxpool` is goroutine-safe. Multiple goroutines can call `Pool.Exec`
concurrently — the pool serialises connections internally.

---

## Memory layout of a sensor reading

```
sse.Reading struct (Go)
├── SensorID  string     (16 bytes header + data)
├── NodeID    string     (16 bytes header + data)
├── Value     float64    (8 bytes)
├── Unit      string     (16 bytes header + data)
├── Quality   int        (8 bytes on amd64)
└── Time      time.Time  (24 bytes)
```

Each reading is ~130–200 bytes on the heap. With 3 sensors × 2/sec × 256-cap
channel per SSE client × 10 clients: ~1.5–3 MB for all channel buffers.
Negligible.

---

## HTTP server configuration

```go
srv := &http.Server{
    ReadTimeout:  10 * time.Second,   // header + body read deadline
    WriteTimeout: 0,                  // NO write deadline — SSE streams are infinite
    IdleTimeout:  120 * time.Second,  // keep-alive connection idle timeout
}
```

**`WriteTimeout: 0` is intentional.** A non-zero write timeout would kill open
SSE connections after the deadline. The keep-alive comment every 15 seconds
prevents proxy-level idle timeouts independently.

---

## Graceful shutdown

```
SIGTERM received
  → cancel(ctx)
      → opcua.Run() exits (context.Done())
  → srv.Shutdown(10s)
      → waits for active /stream goroutines to finish (they detect ctx.Done)
      → waits for active REST handlers to finish
  → db.Close()
  → os.Exit(0)
```

All SSE connections are terminated cleanly. The browser's `EventSource` will
auto-reconnect — if the service restarts quickly enough, clients may not even
notice.

---

## Comparison: Python vs Go implementation

| Concern | Python (asyncua + FastAPI) | Go (gopcua + net/http) |
|---|---|---|
| Concurrency model | asyncio event loop (single thread) | goroutines (M:N scheduler) |
| SSE fan-out | `asyncio.Queue` per client | buffered `chan Reading` per client |
| Blocking risk | Long CPU work blocks the loop | goroutines are preemptively scheduled |
| Memory per SSE client | ~queue overhead + task stack | ~goroutine stack (~2–8 KB) |
| Startup time | ~1–2 s (Python import) | ~50 ms (compiled) |
| Docker image size | ~180 MB (python:3.12-slim + deps) | ~10 MB (distroless/static) |
| Development speed | Faster iteration, less boilerplate | More verbose, stricter types |
| API contract | **Identical** | **Identical** |

Both implementations are correct and production-capable. Pick Go for edge
deployment (tight memory/disk) or Python for rapid extension and data science
integration.

---

## Roadmap (Go-specific)

| Priority | Feature |
|---|---|
| High | OPC UA `SignAndEncrypt` — add `opcua.SecurityPolicy` option in `runOnce` |
| High | API key middleware in `api/handlers.go` |
| Medium | `GET /metrics` — Prometheus counters for readings/sec, SSE clients, DB errors |
| Medium | Worker pool for DB writes (currently synchronous in OPC UA goroutine) |
| Low | Replace `map[string][2]string` sensorMeta with DB-backed or config-file-backed lookup |
