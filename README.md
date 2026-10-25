# Harness Bridge

Go bridge for continuing **existing** OpenCode sessions. Uses the official MCP Go SDK v1.8.0 with authenticated
Streamable HTTP on loopback (MCP 2025-11-25). Remote Claude Chat integration is a
later phase and has not been tested.

On Ubuntu 24.04 ARM64, install Go 1.25 or newer (verified with Go 1.27.2 linux/arm64), clone
this repository, and use an already running OpenCode HTTP server. The bridge
never starts OpenCode or creates a native session.

```sh
export OPENCODE_URL=http://127.0.0.1:4096
export BRIDGE_ALLOWED_ROOTS=/absolute/project/root
export BRIDGE_ALLOWED_SESSIONS=ses_EXISTING_NATIVE_ID
# Writer only: provision an existing 0700 directory owned by the service user.
# Every writer on this host MUST share its inodes; never delete live lock files.
export BRIDGE_WRITER_LOCK_DIR=/var/lib/harness-bridge/writer-locks
# For additional read-only instances instead: BRIDGE_READ_ONLY=1 (no writer lock).
# Set BRIDGE_TOKEN securely (at least 32 characters).
# Set OPENCODE_SERVER_PASSWORD securely if the native server requires Basic auth.
go build -o /tmp/harness-bridge ./cmd/bridge
/tmp/harness-bridge
```

`BRIDGE_ALLOWED_ROOTS` is an OS path-list (`:` on Ubuntu);
`BRIDGE_ALLOWED_SESSIONS` is a comma-separated list of native IDs. Both are
required. The single bearer token represents the local principal with that ACL.
`OPENCODE_SERVER_USERNAME` defaults to `opencode`. Listener defaults to
`127.0.0.1:8080`, endpoint `/mcp`; `BRIDGE_LISTEN` must remain loopback.
Keep tokens in the environment; do not commit them or enable shell tracing.

Tools: `list_sessions`, `get_session`, `read_session_history`, `send_prompt`,
`get_execution_status`, `get_execution_result`, `interrupt_execution`. Use
`opencode:<native-id>` in `session_id` and a unique 8–128 byte idempotency key.
Poll the returned execution ID. An unknown result must not be blindly retried.

Validation:

```sh
gofmt -w cmd internal
go build ./...
go test ./...
go vet ./...
go test -race ./...
```

The deterministic local integration tests use the official SDK client over HTTP
and a native OpenCode API fixture (no provider or credentials required):

```sh
go test ./internal/mcp -run TestSDK -count=1 -v
```

For an explicitly authorized live test, select the existing seeded session and
supply its server credential securely. This command starts only an ephemeral
loopback bridge, connects the official SDK client, and sends two harmless marker
prompts to the existing OpenCode session. It never starts OpenCode or creates a
native session. It validates schemas/discovery, continuity, correlation, cwd,
retained history and duplicate suppression; it does not abort live work.

```sh
export OPENCODE_URL=http://127.0.0.1:4096
export H2_SESSION_ID=ses_REPLACE_WITH_EXISTING_NATIVE_ID
export H2_DIRECTORY=/absolute/existing/session/directory
go run ./cmd/mcpsmoke
```

To exercise the separately running bridge started above, pass its loopback URL
and the same securely supplied `BRIDGE_TOKEN`. Omit `H2_MCP_URL` to use the
embedded ephemeral bridge. Optional idle verification waits 65 seconds without
MCP requests, checks expiration of the one-minute transport session, reconnects
with the official SDK, and verifies history and completed executions remain.
It submits no additional prompts after reconnecting.

```sh
export H2_MCP_URL=http://127.0.0.1:8080/mcp
export H2_TEST_IDLE_EXPIRY=1
go run ./cmd/mcpsmoke
```

See [H2 evidence, exact commands and limits](docs/H2_EVIDENCE.md) and
[H1 evidence and reproducible two-turn smoke test](docs/H1_EVIDENCE.md) for
verified API details, successful live two-turn evidence, and operational limitations.
State and idempotency records are in memory; use one bridge instance with
exclusive ownership of selected sessions. Do not expose this privileged service
through a public listener or tunnel.

H3 authenticated HTTPS configuration, current Claude authentication availability,
operator publication steps and the pending ordinary Claude Chat acceptance test
are documented in [H3 connector guide](docs/H3_CONNECTOR_GUIDE.md) and
[H3 evidence](docs/H3_EVIDENCE.md). No public endpoint was deployed.

Writer ownership and restart/migration requirements: [ADR-010](docs/adr/ADR-010-single-writer.md).
Updated writers fail startup without protected shared ownership configuration;
existing installed services are not upgraded automatically.

H4-A development: [audit and exact capabilities](docs/H4_A_EVIDENCE.md),
[repository registry ADR](docs/adr/ADR-012-repository-aware-session-registry.md),
[native delegation ADR](docs/adr/ADR-013-native-subagent-delegation-guidance.md),
and [future provisioning plan](docs/H4_PROVISIONING_PLAN.md). Four additive
read-only discovery tools; no deployment, session creation or process launch.
