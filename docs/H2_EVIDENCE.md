# H2 implementation and verification

Status: official SDK implementation, deterministic integration and real existing-session
MCP continuation **PASS**. The configured bridge listener and one-minute idle
transport-session expiry were also exercised. Human acceptance/review remains
pending; no merge and no Claude Chat/remote interoperability claim.

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
- `cmd/mcpsmoke` uses an official SDK client against an authenticated loopback
  bridge and an explicitly selected existing OpenCode server/session. It supports
  the embedded bridge or a separately configured listener via `H2_MCP_URL`.
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

## Historical live-test blocker (superseded by authorized run below)

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
This was the initial checkpoint status; the authorized follow-up below supplies
the previously missing real-session MCP evidence.

## Authorized live follow-up (2026-10-24 UTC)

Read the latest review, submitted 2026-10-24 04:40:00 UTC (review node
`PRR_kwDOVVR6Bc8AAAABT_u47Q`). It required real MCP continuation, the configured
HTTP startup/listener and documented idle-session behavior, then fresh quality
checks. The operator explicitly authorized the exact H1 session and secure use
of the current local server credential for this smoke. That supersedes the
historical missing-authorization checkpoint above.

- Existing OpenCode server: PID `3489290`, started 2026-10-23 23:31:25 UTC,
  `opencode serve --hostname 127.0.0.1 --port 4096`. Authenticated health was
  `{"healthy":true,"version":"1.19.4"}`. PID and command were unchanged afterward.
- Selected session `ses_e94de4…[redacted]`, same full ID as operator authorization
  and H1. Metadata directory exactly `/opt/developer/harness-bridge`. Full IDs
  were compared in memory; private history and prompt marker are omitted here.
- Before submission: **6 prior messages**, no incomplete assistant messages, no
  pending session permissions. `/session/status` had no active session entry
  (OpenCode's idle convention); the bridge preserves conservative metadata
  `state:unknown`. No idle/204 response was used as completion evidence.
- Credential was read from the authorized running process environment into
  memory and passed only through child environments for this smoke. Neither the
  credential nor generated bridge token was printed, logged or committed.
- Only a bridge process was started, never a new OpenCode server/session. The
  two prompts explicitly forbade tools and file changes. No live abort, provider
  override, permission response, unknown-outcome retry or replacement session.

Reproducible configuration (credential/token values intentionally omitted):

```sh
go build -o /tmp/h2-bridge ./cmd/bridge
export OPENCODE_URL=http://127.0.0.1:4096
export H2_SESSION_ID=ses_REPLACE_WITH_OPERATOR_AUTHORIZED_EXISTING_ID
export H2_DIRECTORY=/opt/developer/harness-bridge
export BRIDGE_ALLOWED_SESSIONS="$H2_SESSION_ID"
export BRIDGE_ALLOWED_ROOTS="$H2_DIRECTORY"
export BRIDGE_LISTEN=127.0.0.1:18080
# Securely set OPENCODE_SERVER_PASSWORD and a fresh BRIDGE_TOKEN; no shell tracing.
/tmp/h2-bridge
# In another shell with the same secure environment:
export H2_MCP_URL=http://127.0.0.1:18080/mcp
export H2_TEST_IDLE_EXPIRY=1
go run ./cmd/mcpsmoke
```

The run used subprocesses with that environment. The configured executable
reported `Bridge listening on 127.0.0.1:18080`; unauthenticated GET `/mcp` returned
401 before the SDK client connected. Started at **04:41:49 UTC**, finished at
**04:43:04 UTC**, smoke exit **0**. The bridge was then stopped by SIGTERM and
exited **0**; the existing OpenCode server remained running.

Exact smoke output:

```text
native idle preflight verified; no pending permissions or unfinished assistant
existing session selected; native ID and directory redacted; history before prompts: 6 messages
official SDK initialize, tools/list, discovery, metadata and history verified
turn 1 completed; assistant parentID, completion marker, context marker and cwd verified (IDs/content redacted)
turn 2 completed; assistant parentID, completion marker, context marker and cwd verified (IDs/content redacted)
waiting 65 seconds without MCP requests to verify configured idle session expiry
idle MCP session expired; official SDK reconnected; native history and completed executions retained; no prompt resubmission
PASS: same existing native session; earlier history retained; both turns present; directory unchanged; final history: 10 messages
```

The official SDK client negotiated MCP 2025-11-25, listed all seven tools with
schemas, discovered the exact authorized session, read metadata/history,
submitted both prompts, polled status and fetched correlated results. The second
prompt did **not** include the random marker; both assistant outputs exactly
matched it. Same-key retries returned their original execution IDs; changed
payloads were rejected and generated no extra native user message.

Independent authenticated native history inspection after the smoke confirmed:

| Evidence | Turn 1 | Turn 2 |
|---|---|---|
| Native session equals authorized full ID | true | true |
| Assistant parentID equals that turn's user ID | true | true |
| Assistant cwd equals original directory | true | true |
| User time.created (Unix ms) | 1792816910499 | 1792816917145 |
| Assistant time.completed (Unix ms) | 1792816916756 | 1792816918918 |
| Assistant finish | stop | stop |
| Assistant error present | false | false |

History grew **6 → 10**, with all prior message IDs retained and exactly **2** new
user messages. Final session identity/directory matched; no active native status
entry remained. Both completions required correlated terminal assistant proof.

For idle behavior, the client disabled standalone SSE and made no MCP requests
for 65 seconds. The original transport session's tools/list then failed. A new
official SDK connection to the same running listener succeeded, read the same
10 history message IDs and retrieved both original executions as `completed`.
Thus expiry did not destroy native history or execution records and required no
prompt resubmission. This test covers already completed executions across idle
expiry; it does not claim live proof of a running model surviving expiry (detached
worker behavior remains covered by the regression tests).

## Quality gates after authorized live test

All commands below exited **0** after the live run on 2026-10-24 UTC. Build and vet
were silent. Existing test-package results were cached because their source and
dependencies were unchanged; the updated smoke command compiled in both normal
and race builds and was directly exercised live above. No test failure or race
report occurred.

```text
$ gofmt -w cmd internal
$ go build ./...
$ go test ./...
?   	github.com/nevzatcirak/harness-bridge/cmd/bridge	[no test files]
?   	github.com/nevzatcirak/harness-bridge/cmd/mcpsmoke	[no test files]
?   	github.com/nevzatcirak/harness-bridge/cmd/smoke	[no test files]
ok  	github.com/nevzatcirak/harness-bridge/internal/adapters/opencode	(cached)
ok  	github.com/nevzatcirak/harness-bridge/internal/execution	(cached)
ok  	github.com/nevzatcirak/harness-bridge/internal/mcp	(cached)
?   	github.com/nevzatcirak/harness-bridge/internal/mcpclient	[no test files]
ok  	github.com/nevzatcirak/harness-bridge/internal/security	(cached)
$ go vet ./...
$ go test -race ./...
?   	github.com/nevzatcirak/harness-bridge/cmd/bridge	[no test files]
?   	github.com/nevzatcirak/harness-bridge/cmd/mcpsmoke	[no test files]
?   	github.com/nevzatcirak/harness-bridge/cmd/smoke	[no test files]
ok  	github.com/nevzatcirak/harness-bridge/internal/adapters/opencode	(cached)
ok  	github.com/nevzatcirak/harness-bridge/internal/execution	(cached)
ok  	github.com/nevzatcirak/harness-bridge/internal/mcp	(cached)
?   	github.com/nevzatcirak/harness-bridge/internal/mcpclient	[no test files]
ok  	github.com/nevzatcirak/harness-bridge/internal/security	(cached)
$ git diff --check
```

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
