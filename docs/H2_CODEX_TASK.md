# H2 — Official MCP Go SDK

Status: Codex implementation assignment. **Never merge your own PR.**

## Goal
Replace the provisional local JSON-RPC MCP shim with the official `github.com/modelcontextprotocol/go-sdk` implementation, maintaining H1's validated existing-session behavior.

## Scope
1. Read HARNESS_BRIDGE_SPEC.md, docs/ARCHITECTURE.md, docs/CONTRACTS.md, docs/H1_EVIDENCE.md and the existing implementation before changes.
2. Verify current official MCP Go SDK documentation and versions; pin an appropriate compatible version in go.mod/go.sum. Use idiomatic Go. No custom hand-rolled wire protocol.
3. Register the existing seven tools with typed input/output schemas: list_sessions, get_session, read_session_history, send_prompt, get_execution_status, get_execution_result, interrupt_execution.
4. Preserve OpenCode native session identity, history, directory routing, async detached execution, guard/idempotency semantics and strict session/project-root authorization from H1. Do not weaken or bypass permission controls.
5. Use loopback-only local MCP HTTP transport for H2. Maintain explicit authentication and add protocol-level testing, including initialize, tools/list, tools/call, valid/invalid JSON schema, errors and caller cancellation.
6. Provide a runnable local MCP client integration test using the official SDK. Reuse the existing OpenCode session only when it is safe and explicitly authorized; do not manufacture completion evidence.
7. Update README, CONTRACTS, ARCHITECTURE and H2_EVIDENCE with exact reproducible build/test commands and limits.
8. Run gofmt, go build ./..., go test ./..., go vet ./..., go test -race ./... and include exact outcomes.
9. Conventional Commits to this branch, push, update PR #2, stop for human/architectural review.

## Out of scope
Internet-facing tunnel, OAuth integration with Claude Chat, Claude Code adapter, DB, broker, UI, durable agent supervisor. H2 is *not* proof of actual Claude Chat interoperability. Never bind privileged MCP unauthenticated to 0.0.0.0.

## Acceptance gate
- [ ] Official Go SDK is used for server/tool registration and JSON Schema.
- [ ] Seven tools have stable schema and structured results/errors.
- [ ] End-to-end local SDK client ↔ bridge ↔ OpenCode session test passes.
- [ ] H1 existing-session continuation and security regression tests remain passing.
- [ ] Timeout, authorization and duplicate guards preserved.
- [ ] Go quality gates pass.
- [ ] Known gaps documented; no false remote/Claude compatibility claims.

Reviewers handle merge and next phase.
