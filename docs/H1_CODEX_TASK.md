# H1 — OpenCode Existing Session Continuation

**Status:** implementation assignment — not acceptance evidence.
**Owner:** Codex CLI on Hetzner Ubuntu 24.04 ARM64.
**Base:** `main`. Work ONLY on `feat/h1-opencode-session-continuation`.

## Objective

Demonstrate that the Go Harness Bridge attaches to an *already running* OpenCode HTTP server, discovers an *existing* native session, reads its history, submits prompt #1, observes a terminal result, then submits prompt #2 to the SAME native session and verifies preserved history and working directory.

## Required tasks

1. Read `HARNESS_BRIDGE_SPEC.md`, `docs/ARCHITECTURE.md`, `docs/CONTRACTS.md` and current code. Preserve project decisions and avoid unrelated features.
2. Confirm installed OpenCode version and **actual** HTTP schemas on this server. Do not assume that an HTTP 204 proves completion; do not equate idle with success. Document any mismatch with prototype.
3. Repair implementation in idiomatic Go. Focus on native message IDs/correlation, status transitions, unknown/disconnected states, timeouts and context cancellation, busy guarding, duplicate suppression, and explicit interruption. Do not retry uncertain prompt submissions blindly.
4. Ensure tools use stable `opencode:<native-id>` session IDs externally and native IDs internally. Validate inputs and enforce allowlisted project root(s), per-session authorization, and local-only default. Do not bypass OpenCode permission prompts. Do not expose a privileged HTTP endpoint publicly.
5. Add unit/httptest coverage for discovery, history, prompt, status, results, errors, concurrency, conflicting idempotency keys, missing status, and cancellation.
6. Add a small Go integration smoke-test or documented reproducible commands, operated against the **already running** OpenCode server. The test must not silently start a separate server or replace the target existing session.
7. Document launch/configuration on Ubuntu 24.04 ARM64, redacting secrets. Do not commit passwords, provider tokens, personal message history or private repository data.
8. Verify `go build ./...`, `go test ./...`, `go vet ./...`, and `go test -race ./...`. Capture exact outputs and OpenCode version.
9. Report known limitations and acceptance evidence in the PR. Never claim Claude Chat integration was tested; that is a later phase.

## Acceptance criteria

- [ ] Server process identity and base URL are explicitly documented; no accidental new server
- [ ] Existing session selected, native ID + directory recorded in **redacted** evidence
- [ ] History read before any prompt
- [ ] Prompt #1 submitted and resulting assistant message unambiguously correlated
- [ ] Completion/error/permission states not inferred from HTTP 204 or idle alone
- [ ] Prompt #2 submitted to exact same native session ID
- [ ] Session history shows both turns, with working directory unchanged
- [ ] Duplicate key does not trigger another native prompt; same key with different payload conflicts
- [ ] Busy session rejects concurrent submissions
- [ ] Timeout, disconnect, uncertainty, interrupt and auth failure paths tested
- [ ] Build, unit, vet and race commands pass; exact evidence in PR

## Out of scope

Claude Code adapter, internet-facing connector, autonomous Supervisor Runtime, database, broker, frontend, Kubernetes and general multi-agent orchestration. Do not merge this PR. A reviewer will request changes or accept it.

## Handoff

When implementation ends: push commits using Conventional Commits (`feat:`, `fix:`, `test:`, `docs:`, `refactor:`), edit PR description with evidence and limitations, then stop. User will request reviewer inspection; follow PR review comments in the same branch.
