# Harness Bridge
Go-based experimental bridge for **existing** OpenCode sessions. This is a **local bootstrap**, not a supported production remote MCP connector. Do not expose it to the public internet.

## Start
Set `BRIDGE_TOKEN` (at least 32 characters), `OPENCODE_URL` (loopback HTTP URL for an already-running OpenCode server), and optionally `OPENCODE_SERVER_PASSWORD`.
Run `go run ./cmd/bridge`. Default listener is `127.0.0.1:8080`, endpoint `/mcp`.

## Quality gates
`go build ./...`, `go test ./...`, `go vet ./...`, `go test -race ./...`.

## Limitations
Current execution state is in memory and may become unknown after restart or disconnection. Status and message correlation must be verified against a live OpenCode server before production use. Claude Custom Connector and official MCP Go SDK integration are pending. Local prototype uses a narrow HTTP/JSON-RPC transport shim and **must not** be publicly exposed.

See `docs/ARCHITECTURE.md` and `HARNESS_BRIDGE_SPEC.md`.
