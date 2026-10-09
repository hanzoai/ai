# Kai Unix socket adapter (transition only)

This Go package implements the **existing** Kai Rust socket protocol from
`hanzo-inc/kai/decision/src/serve.rs`, as inspected 2026-10-08:

- Three request frames: `org`, `capabilities`, `body`.
- Each frame: little-endian `u32` payload length, followed by raw bytes.
- One response frame, raw JSON. The API request body is still Decisions JSON.
- 256-character org limit; 256 KiB capability frame; 16 MiB request frame.

It is **not ZAP** and it is not MCP. The real ZAP binary RPC Rust implementation
lives at `hanzoai/zap` (`zapwire`), with a different message header/envelope.
The old Kai socket contains neither HTTP status codes nor retry headers, and
does not support the HTTP-only `X-Capture: 1` behavior. Therefore this adapter
is **not** automatically wired into `controllers/decisions.go`.

Never silently fall back from an ambiguous UDS failure to a second HTTP
decision call: the first call may have completed, causing duplicate execution
or metering. Before activating the path, implement idempotency and equivalent
error/capture/usage semantics and integration tests. Keep the socket restricted
to trusted sidecar processes by filesystem permissions. Request org identity
must originate from the gateway's authenticated context, never client JSON.

### Test

```sh
go test ./internal/kaiuds
go test -race ./internal/kaiuds
```

### Example (not wired to live traffic)

```go
client := kaiuds.Client{Path: "/run/kai/kai.sock"}
body, err := client.Decide(ctx, authenticatedOrg, attachedCapabilities, decisionJSON)
```

Remote Rust ZAP currently has no session TLS in the `hanzoai/zap` README.
Use authorized, authenticated HTTPS to remote GPU workers until mutual
authentication and encryption are implemented and tested.
