# H1 implementation and verification

Status: implementation complete; **live two-turn acceptance BLOCKED** because the
already running server contains no existing sessions. Do not mark H1 accepted or
claim this PR is ready for acceptance until the live smoke test succeeds.

## Verified server (2026-10-23 UTC)

- Ubuntu host, linux/arm64; `go version go1.27.2 linux/arm64`.
- `opencode --version`: `1.19.4`.
- Existing PID `3444808`, started `Fri Oct 23 23:05:19 2026`, command
  `opencode serve --hostname 127.0.0.1 --port 4096`.
- Base URL `http://127.0.0.1:4096`. No OpenCode server or session was created.
- Unauthenticated `GET /global/health`: `401 Unauthorized` with Basic challenge.
  Authenticated discovery used the existing process credential without logging it.
- Authenticated `GET /doc` supplied the actual OpenAPI schema. It was inspected
  locally; the complete document and credentials are not committed.
- `/session`, `/experimental/session`, and
  `/session?directory=/opt/developer/harness-bridge`: `[]`.
- `/api/session`: `data` count `0` (global v2 index also empty).
- Native ID: **unavailable**. Directory: **unavailable**. History, native prompt
  submission, model execution and two-turn continuity are **not tested live**.
  The operator must supply an existing native session and its directory.

## API facts and repaired prototype assumptions

The actual schema confirms:

- `GET /session/{sessionID}`, `/message`, `/session/status`, `/permission`;
  directory routing is the `directory` query parameter.
- `POST /session/{sessionID}/prompt_async` accepts `messageID` matching `^msg`
  and text parts; `204` only acknowledges submission. Prototype random hex IDs
  lacked the required prefix. Native ascending message IDs encode a clock;
  bridge IDs now retain that layout to avoid reordering history.
- Assistant messages expose `sessionID`, `parentID`, `time.completed`, `finish`,
  `error`, and `path.cwd`. Replies must match the submitted message and session.
  A completed assistant with `stop`/`end_turn` is terminal success; `tool-calls`
  is intermediate. A correlated completed error is failed/interrupted.
- Native status schema allows `idle`, `busy`, `retry`, not a permission state.
  Pending permissions come from `/permission`; bridge never answers them.
- Missing status without terminal assistant proof is `unknown`. Idle or 204 alone
  never completes a run. A correlated terminal message can prove completion when
  status is absent; an explicitly busy/retry status retains the guard.
- `/abort` returns a boolean. An accepted abort retains the guard until a
  correlated terminal message; it does not immediately assert interruption.

Reference: [official server API](https://docs.opencode.ai/docs/server/) and
[official ID implementation](https://github.com/anomalyco/opencode/blob/dev/packages/opencode/src/id/id.ts).
The locally served schema, rather than an assumed SDK version, governed this work.

## Behavior and security

- External session IDs are `opencode:<native-id>`; native requests use native IDs.
- One static bearer token represents one local principal. Explicit native session
  ACL plus canonical project root checks protect discovery, history, prompts,
  execution reads/results and interruption. Symlink escapes are denied.
- Requests use the selected session's stored directory, never a caller-supplied
  replacement. Existing server/session creation APIs are absent from the bridge.
- Prompt acceptance returns `submitting` with an execution ID. A detached bounded
  worker submits once and polls independently of client connection lifetime.
- Same session/key/prompt returns the same execution; changed payload conflicts.
  A per-session reservation rejects simultaneous/uncertain runs. Unrelated
  sessions do not share a network IO lock.
- Before acceptance: cancellation releases the reservation without a native POST.
  After acceptance: client cancellation does not cancel execution. Explicit
  interruption uses the native abort endpoint without granting permissions.
- Transport failure after submission is unknown and never automatically retried.
  Monitoring disconnects can recover from correlated proof during the five-minute
  monitoring budget. Timeout/shutdown preserve uncertainty and the session guard.
- Loopback-only upstream and listener; upstream redirects and environment proxies
  disabled. Error envelopes redact upstream response bodies; audit logs contain
  IDs/dispositions rather than prompts, credentials or message content.

## Reproduce the live test

Use the existing authenticated server. Do not create a replacement session to
turn an empty discovery result into a passing existing-session test.

```sh
export OPENCODE_URL=http://127.0.0.1:4096
# Set OPENCODE_SERVER_PASSWORD securely in your shell, if required; do not log it.
export H1_SESSION_ID=ses_REPLACE_WITH_EXISTING_NATIVE_ID
export H1_DIRECTORY=/absolute/existing/session/directory
go run ./cmd/smoke
```

The Go smoke command verifies discovery, metadata and history before submitting
anything. It sends two harmless prompts into the exact same existing session;
turn two must recall a random marker present only in turn one. It checks both
assistant parent IDs, completion, assistant cwd, unchanged session metadata,
retention of earlier history, both native user IDs, duplicate suppression and
conflicting-key rejection. Its output redacts session/message IDs, paths and
message contents. No provider override or permission bypass is used.

## Automated coverage and quality gates

Tests cover native HTTP schemas, directory routing, discovery/history/results,
parent correlation, two turns, idle/204/intermediate-message insufficiency,
missing status, connection loss/recovery, actual HTTP timeout, canceled request,
uncertain POST, duplicate key/conflicting payload, concurrent duplicates and
busy protection, permission waits, explicit interruption, abort rejection,
upstream 401/403/404/500, redirect rejection, malformed/oversized responses,
strict input parsing, bearer authentication, per-session ACL and symlink roots.

Exact final command output is recorded below after validation.

## Known limitations

- **No existing session was available: live two-turn acceptance remains unmet.**
- Claude Chat, remote MCP interoperability, OAuth and official MCP Go SDK
  integration are not tested or delivered in H1. The transport remains a local
  JSON-RPC bootstrap. Do not expose it publicly.
- State/idempotency/guards are in memory, with no restart recovery or eviction.
  Use a single bridge instance and exclusive session ownership. Native TUI or
  other clients can race with preflight checks; the native API provides no atomic
  cross-client session reservation. Bridge guards do not lock external clients.
- Uncertain submission and exhausted monitoring budgets keep the session guarded
  for the lifetime of this process. There is no unsafe unlock/retry endpoint;
  operator reconciliation with native history/status is necessary. Restart loses
  the guard and deduplication records; never treat restart as proof of completion.
- Default request timeout is ten seconds; monitor budget is five minutes with
  500 ms polling. No SSE reconnect/replay or durable supervisor is implemented.
- History is read with an 8 MiB response bound. Pagination is not implemented;
  oversized histories fail closed. Outputs are native message parts, not a claim
  that the coding task's acceptance criteria were independently satisfied.
- Unsupported finish values remain running/unknown until timeout. Question waits
  and v2 workspace APIs are not normalized; permissions use the verified v1 API.

## Exact final validation output (2026-10-23 UTC)

All four commands exited 0. Build and vet produced no stdout/stderr.

```text
$ go build ./...
$ go test ./...
?   	github.com/nevzatcirak/harness-bridge/cmd/bridge	[no test files]
?   	github.com/nevzatcirak/harness-bridge/cmd/smoke	[no test files]
ok  	github.com/nevzatcirak/harness-bridge/internal/adapters/opencode	0.070s
ok  	github.com/nevzatcirak/harness-bridge/internal/execution	0.076s
ok  	github.com/nevzatcirak/harness-bridge/internal/mcp	0.015s
ok  	github.com/nevzatcirak/harness-bridge/internal/security	(cached)
$ go vet ./...
$ go test -race ./...
?   	github.com/nevzatcirak/harness-bridge/cmd/bridge	[no test files]
?   	github.com/nevzatcirak/harness-bridge/cmd/smoke	[no test files]
ok  	github.com/nevzatcirak/harness-bridge/internal/adapters/opencode	1.220s
ok  	github.com/nevzatcirak/harness-bridge/internal/execution	(cached)
ok  	github.com/nevzatcirak/harness-bridge/internal/mcp	(cached)
ok  	github.com/nevzatcirak/harness-bridge/internal/security	(cached)
```
