# MVP Development Backlog

| Order | Task | Acceptance | Risk |
|---|---|---|---|
| 0 | Verify OpenCode `/doc` from exact user's running version, message IDs, async state, SSE | Captured request/response traces | Undocumented linkage |
| 1 | Core Go OpenCode adapter | Existing server and session list/history; two-turn integration | Different server instance |
| 2 | Execution state and idempotency hardening | Busy, duplicate, disconnected, timeout and restart tests | Ambiguous submission |
| 3 | Official MCP Go SDK v1.8 migration | `go build/test/vet/race`, MCP inspector + transport tests | SDK compatibility |
| 4 | Workspace and per-session authorization | Cross-root and cross-principal denied | Wrong session |
| 5 | Secure remote HTTPS ingress and OAuth or verified fixed-token security | No unauthenticated access; actual Claude connector works | Public exposure |
| 6 | Real Claude Chat multi-turn experiment | Reads original history, sends prompt 1, observes completion, sends prompt 2 to same native ID | Claude tool loop limitations |
| 7 | Evidence and recovery | Tests/diffs verified, server restart behavior documented | False completion |

Not in MVP: Claude Code adapter, database, broker, frontend, external reasoning supervisor, automatic permission grant.