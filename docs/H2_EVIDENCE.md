# H2 implementation and verification

Status: official SDK implementation and deterministic local integration **PASS**.
Real existing-session MCP continuation is **not tested in this H2 run**; its live
acceptance gate remains open. PR #2 remains draft for architectural review. No
merge and no Claude Chat/remote interoperability claim.

## Implementation and official sources

- Pinned `github.com/modelcontextprotocol/go-sdk v1.8.0` and its schema engine
  `github.com/google/jsonschema-go v0.4.3`; committed module checksums. Go minimum
  changed from 1.23 to 1.25 because SDK v1.8.0 requires Go 1.25.
- Verified the [official releases](https://github.com/modelcontextprotocol/go-sdk/releases/tag/v1.8.0)
  and [SDK documentation/source](https://github.com/modelcontextprotocol/go-sdk/tree/v1.8.0)
  and inspected the downloaded tagged `mcp/server.go`, `mcp/streamable.go`,
  `mcp/client.go`, and SDK go.mod. v1.8.0 was the latest stable release observed.
- Official `mcp.AddTool` registers seven typed tools; strict schemas are inferred
  from Go structs, with explicit ID/length constraints. Output schemas validate
  both H1 success shapes and typed domain failures. Raw native parts/errors retain
  arbitrary JSON sub-schemas. SDK-generated schema failures remain diagnostic
  text tool errors, distinct from structured domain failures.
- Official Streamable HTTP owns MCP negotiation, sessions, dispatch, errors and
  cancellation. H2 explicitly advertises MCP 2025-11-25, ensuring the SDK client
  uses initialize/initialized. SDK v1.8.0 also supports a newer stateless protocol;
  that protocol is deliberately outside this H2 configuration.
- Bearer authentication wraps every method. SDK DNS rebinding protection and Go
  cross-origin protection are enabled. H1's 1 MiB, single-JSON-value body guard is
  retained: the SDK alone accepted a trailing JSON value during development, so
  generic HTTP JSON framing validation rejects it before MCP decoding.
- H1 adapter, execution manager and security policy source are unchanged. Native
  identity, stored directory routing, ACL/root checks, permission handling,
  detached worker, correlation, idempotency and uncertain-outcome guards remain.
- `cmd/mcpsmoke` uses an official SDK client against an ephemeral authenticated
  loopback bridge and an explicitly selected existing OpenCode server/session.
  It checks metadata/history, sends two harmless prompts, verifies marker recall,
  parent/session/cwd continuity, retained prior history and idempotency. It does
  not create native sessions/servers or interrupt live work. Its random bearer
  token is independent of the prompt marker; audit identifiers are redacted.

## Deterministic local integration

The command below ran with Go 1.27.2 linux/arm64, SDK v1.8.0. It exited 0 on
2026-10-23 at approximately 23:54:36 UTC:

```sh
go test ./internal/mcp -run TestSDK -count=1 -v
```

The official SDK client traversed loopback TCP HTTP -> bridge auth -> SDK MCP ->
policy -> execution manager -> native HTTP adapter -> OpenCode API **fixture**.
The fixture starts with an existing seeded native session and returns controlled
native responses. It is not a real OpenCode process or model and supplies no
proof of live model context recall. No production credential is needed.

Exact test-result lines (routine fixture-ID audit lines omitted):

```text
--- PASS: TestSDKLocalIntegration (0.08s)
--- PASS: TestSDKCancellationAndInterrupt (0.04s)
--- PASS: TestSDKHTTPGuards (0.52s)
    --- PASS: TestSDKHTTPGuards/missing_auth (0.00s)
    --- PASS: TestSDKHTTPGuards/malformed_JSON (0.00s)
    --- PASS: TestSDKHTTPGuards/trailing_JSON (0.00s)
    --- PASS: TestSDKHTTPGuards/oversized (0.00s)
    --- PASS: TestSDKHTTPGuards/DNS_rebinding (0.00s)
    --- PASS: TestSDKHTTPGuards/cross_origin (0.00s)
--- PASS: TestSDKExecutionAuthorization (0.04s)
PASS
ok  	github.com/nevzatcirak/harness-bridge/internal/mcp	0.687s
```

Coverage includes initialize protocol/server identity; seven tools with strict
input and success/error output schemas; tools/list and tools/call; valid inputs;
missing/unexpected fields, wrong types, malformed IDs, short keys and oversized
prompts; SDK unknown-tool errors; structured FORBIDDEN/NOT_FOUND errors; two-turn
history/correlation/cwd; duplicate suppression and key conflict; native busy
guard; MCP cancellation before acceptance reaching upstream without a prompt
POST; caller context cancellation after acceptance retaining detached execution;
explicit interrupt and correlated aborted result. A separate principal with a
known execution ID but an unauthorized root cannot inspect or interrupt it.
HTTP checks cover auth on POST/GET/DELETE (even with a valid MCP session ID),
malformed/trailing JSON, oversized bodies, DNS rebinding and cross-origin POST.

All original H1 adapter/execution/security tests remain present and passing,
including timeout, uncertain submission, busy/duplicate guards, permissions,
connection loss/recovery, native error redaction, redirect denial, symlink root
escapes and before/after-acceptance cancellation.

## Required quality gates

Exact commands, all exited 0. `gofmt`, build and vet were silent. The final source
checks followed the execution-authorization regression addition; subsequent
changes were documentation only. Network/module downloads and loopback tests
required the environment's approved sandbox escalation. Initial sandbox-only
attempts failed on DNS, read-only Go cache or socket access; these were environment
failures, followed by successful approved runs.

```text
$ gofmt -w cmd internal
$ go build ./...
$ go test ./...
?   	github.com/nevzatcirak/harness-bridge/cmd/bridge	[no test files]
?   	github.com/nevzatcirak/harness-bridge/cmd/mcpsmoke	[no test files]
?   	github.com/nevzatcirak/harness-bridge/cmd/smoke	[no test files]
ok  	github.com/nevzatcirak/harness-bridge/internal/adapters/opencode	(cached)
ok  	github.com/nevzatcirak/harness-bridge/internal/execution	(cached)
ok  	github.com/nevzatcirak/harness-bridge/internal/mcp	0.712s
?   	github.com/nevzatcirak/harness-bridge/internal/mcpclient	[no test files]
ok  	github.com/nevzatcirak/harness-bridge/internal/security	(cached)
$ go vet ./...
$ go test -race ./...
?   	github.com/nevzatcirak/harness-bridge/cmd/bridge	[no test files]
?   	github.com/nevzatcirak/harness-bridge/cmd/mcpsmoke	[no test files]
?   	github.com/nevzatcirak/harness-bridge/cmd/smoke	[no test files]
ok  	github.com/nevzatcirak/harness-bridge/internal/adapters/opencode	(cached)
ok  	github.com/nevzatcirak/harness-bridge/internal/execution	(cached)
ok  	github.com/nevzatcirak/harness-bridge/internal/mcp	2.224s
?   	github.com/nevzatcirak/harness-bridge/internal/mcpclient	[no test files]
ok  	github.com/nevzatcirak/harness-bridge/internal/security	(cached)
$ git diff --check
```

## Live test status and reproduction

H1 recorded successful real two-turn continuation in H1_EVIDENCE. That evidence
is preserved; it does not establish H2 MCP live success. This run's environment
had no OPENCODE_URL, OPENCODE_SERVER_PASSWORD, H1/H2_SESSION_ID or H1/H2_DIRECTORY.
A separate explicit authorization question was sent for reusing the H1 native
session and reading its running server credential without printing/storing it.
No answer had arrived at this checkpoint. No process credential was read and no
live prompt was sent. H2's task explicitly requires safe, authorized session reuse.

After authorization, use the same existing seeded session, verify it is idle and
not awaiting permissions, and securely supply the current credential:

```sh
export OPENCODE_URL=http://127.0.0.1:4096
export H2_SESSION_ID=ses_REPLACE_WITH_EXISTING_NATIVE_ID
export H2_DIRECTORY=/absolute/existing/session/directory
# Securely set OPENCODE_SERVER_PASSWORD if required; never enable shell tracing.
go run ./cmd/mcpsmoke
```

The command exits on identity/directory mismatch, missing prior history, busy or
permission-pending preflight. A failed/unknown mutation must be reconciled rather
than rerun blindly. Do not create a replacement session to manufacture evidence.
The real-session MCP acceptance item remains unchecked until actual terminal,
parent-correlation, history and cwd evidence is captured.

## Remaining limitations

- Loopback only; static token/single principal; no public tunnel, OAuth, actual
  Claude Chat integration, Claude Code adapter or remote interoperability proof.
- H1 in-memory state, five-minute monitor budget, no durable recovery/eviction,
  uncertain-outcome reservation and exclusive session ownership limits persist.
  A transport reconnection does not authorize a fresh mutation key. Restart loses
  idempotency/guards; restart is never proof of completion.
- Native TUI/other clients can race with preflight; no atomic native reservation.
  History has an 8 MiB upstream bound and no pagination; large output may exceed
  a client's configured limits. Raw native JSON is not independently validated
  evidence that the user's coding acceptance criteria were met.
- Idle MCP sessions expire after one minute; accepted native executions continue.
  Cancellation testing uses the official SDK cancellation notification path for
  2025-11-25. Bare HTTP socket loss is not promised to cancel preflight in that
  SDK protocol mode. Explicit authorized interruption is required after acceptance.
- The new SDK client helper is a local integration utility, not a reconnecting
  supervisor. No SSE replay/resumption, newer stateless protocol or conformance
  certification is claimed. Human reviewers decide acceptance and merge.
