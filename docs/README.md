# Documentation — opcua-railway-go

| Document | Description |
|---|---|
| [01 — OPC UA Explained](./01-opc-ua-explained.md) | What OPC UA is, the subscription model, NodeIds, StatusCodes, security modes |
| [02 — Architecture](./02-architecture.md) | System diagram, Go concurrency model, goroutine layout, Python vs Go comparison |
| [03 — Module Reference](./03-modules.md) | Purpose, exported API, and design notes for every Go package |
| [04 — Security Guide](./04-security.md) | OPC UA SignAndEncrypt, HTTP auth, HTTPS, DB hardening, network isolation |
| [05 — Frontend API Contract](./05-api-contract.md) | SSE stream spec, REST endpoint schemas, browser integration example |

> The companion Python implementation lives at
> [ahmadbass3l/opcua-railway-python](https://github.com/ahmadbass3l/opcua-railway-python)
> and exposes an identical API.
