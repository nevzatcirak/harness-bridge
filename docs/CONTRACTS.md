# MCP contracts: proposed v0.1 and implemented H2 subset

Common rules: tool input objects have `additionalProperties:false`; all IDs are strings; session_id must use `opencode:<native-id>` in final production interface; maximum prompt length 32 KiB; page size max 100; structured tool errors must contain `{code,message,retryable,execution_id?}`; authenticated principal is taken from validated transport identity, never tool arguments.

## Input JSON Schema

```json
{
  "$defs": {
    "sessionId": {"type":"string","pattern":"^opencode:[A-Za-z0-9_-]+$"},
    "executionId": {"type":"string","minLength":1},
    "idempotencyKey": {"type":"string","minLength":8,"maxLength":128}
  },
  "tools": {
    "list_sessions": {"type":"object","properties":{"limit":{"type":"integer","minimum":1,"maximum":100},"cursor":{"type":"string"}},"additionalProperties":false},
    "get_session": {"type":"object","properties":{"session_id":{"$ref":"#/$defs/sessionId"}},"required":["session_id"],"additionalProperties":false},
    "read_session_history": {"type":"object","properties":{"session_id":{"$ref":"#/$defs/sessionId"},"limit":{"type":"integer","minimum":1,"maximum":100},"cursor":{"type":"string"}},"required":["session_id"],"additionalProperties":false},
    "send_prompt": {"type":"object","properties":{"session_id":{"$ref":"#/$defs/sessionId"},"prompt":{"type":"string","minLength":1,"maxLength":32768},"idempotency_key":{"$ref":"#/$defs/idempotencyKey"}},"required":["session_id","prompt","idempotency_key"],"additionalProperties":false},
    "get_execution_status": {"type":"object","properties":{"execution_id":{"$ref":"#/$defs/executionId"}},"required":["execution_id"],"additionalProperties":false},
    "get_execution_result": {"type":"object","properties":{"execution_id":{"$ref":"#/$defs/executionId"}},"required":["execution_id"],"additionalProperties":false},
    "interrupt_execution": {"type":"object","properties":{"execution_id":{"$ref":"#/$defs/executionId"},"reason":{"type":"string","maxLength":1024}},"required":["execution_id"],"additionalProperties":false}
  }
}
```

## Output JSON Schema (shared definitions)

```json
{
  "$defs": {
    "session": {"type":"object","required":["session_id","native_id","harness","directory","state"],"properties":{"session_id":{"type":"string"},"native_id":{"type":"string"},"harness":{"const":"opencode"},"directory":{"type":"string"},"title":{"type":"string"},"state":{"enum":["idle","running","waiting_for_permission","unknown"]}},"additionalProperties":true},
    "execution": {"type":"object","required":["execution_id","session_id","state","started_at","updated_at"],"properties":{"execution_id":{"type":"string"},"session_id":{"type":"string"},"message_id":{"type":"string"},"state":{"enum":["submitting","running","waiting_for_permission","completed","failed","interrupted","unknown"]},"started_at":{"type":"string","format":"date-time"},"updated_at":{"type":"string","format":"date-time"},"error":{"type":"string"}},"additionalProperties":true},
    "message": {"type":"object","required":["message_id","role","parts"],"properties":{"message_id":{"type":"string"},"role":{"type":"string"},"parts":{"type":"array","items":{}}}},
    "error": {"type":"object","required":["code","message","retryable"],"properties":{"code":{"enum":["UNAUTHORIZED","FORBIDDEN","NOT_FOUND","SESSION_BUSY","IDEMPOTENCY_CONFLICT","INVALID_INPUT","UPSTREAM_TIMEOUT","UNKNOWN_OUTCOME","UPSTREAM_FAILURE"]},"message":{"type":"string"},"retryable":{"type":"boolean"},"execution_id":{"type":"string"},"attempt_id":{"type":"string"},"attempt":{"type":"integer"},"reason":{"type":"string"}}}
  },
  "outputs": {
    "list_sessions": {"type":"object","required":["sessions"],"properties":{"sessions":{"type":"array","items":{"$ref":"#/$defs/session"}},"next_cursor":{"type":"string"}}},
    "get_session": {"$ref":"#/$defs/session"},
    "read_session_history": {"type":"object","required":["messages"],"properties":{"messages":{"type":"array","items":{"$ref":"#/$defs/message"}},"next_cursor":{"type":"string"}}},
    "send_prompt": {"$ref":"#/$defs/execution"},
    "get_execution_status": {"$ref":"#/$defs/execution"},
    "get_execution_result": {"type":"object","required":["execution","messages"],"properties":{"execution":{"$ref":"#/$defs/execution"},"messages":{"type":"array","items":{"$ref":"#/$defs/message"}},"diff":{"type":"array","items":{}},"evidence":{"type":"array","items":{}}}},
    "interrupt_execution": {"$ref":"#/$defs/execution"}
  }
}
```

Important: these are design contracts, not a claim that every field, pagination feature or schema is implemented by the bootstrap. Output errors and limits still require enforcement in production.
## H1 implemented subset

H1 keeps the seven bootstrap tools with strict argument parsing. List/history
return `{sessions:[...]}` and `{messages:[...]}`. Session metadata contains
`session_id`, `native_id`, `harness`, `directory`, `title`, and `state`
(H1–H3: conservatively `unknown`; from H4-A see "Session state and the Bridge
guard" below; execution state is separate). Execution IDs and message IDs are
separate; execution `session_id` is always `opencode:<native-id>`. Status/result
reads are snapshots of a background monitor. Result messages include only the
submitted user message and assistant messages whose native session and parent ID
match; they may include intermediate tool-call messages.

Tool failures contain a JSON error envelope in the text content with `isError`.
Native response bodies are not copied into generic errors. HTTP transport auth
failures are HTTP 401. No cursor/limit/reason arguments are advertised by this
subset: supplying unknown fields fails parsing. History responses have an 8 MiB
bound; pagination remains deferred. Interruption is explicit and acknowledgement
does not itself mark an execution interrupted. See `H1_EVIDENCE.md` for lifecycle,
idempotency, timeout and restart semantics and successful live-test evidence.


## H2 implemented MCP contract (v0.2)

This section supersedes the bootstrap transport described above. The earlier
pagination/normalized-message schemas remain proposals, not advertised features.
`tools/list` is the executable source for the implemented JSON Schemas, generated
from Go structs in `internal/mcp/tools.go` and existing execution/native DTOs.

| Tool | Required input fields | Success structuredContent |
|---|---|---|
| list_sessions | none (empty object) | `{sessions: Session[]}` |
| get_session | session_id | `Session` |
| read_session_history | session_id | `{messages: NativeMessage[]}` |
| send_prompt | session_id, prompt, idempotency_key | `Execution` |
| get_execution_status | execution_id | `Execution` |
| get_execution_result | execution_id | `{execution: Execution, messages: NativeMessage[]}` |
| interrupt_execution | execution_id | `Execution` |

All inputs reject additional fields and require strings for declared fields.
Session IDs match `^opencode:ses[A-Za-z0-9_-]+$` or the Claude Code form
`^claudecode:[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$` (ADR-021
§9; an `anyOf`, schema only: a `claudecode:` ID is refused with `INVALID_INPUT` until
the adapter is enabled, which it is not by default), max 265 characters, with native
ID validation and authorization enforced again at execution. Execution IDs must
be nonempty. Prompt schema length is 1–32768 characters; H1 additionally rejects
blank prompts and enforces its stricter 32768-byte bound. Keys have schema length
8–128 characters and retain the 8–128-byte execution guard. There is no
cursor/limit/reason argument; history remains bounded by the 8 MiB upstream read.

Session fields: `session_id`, `native_id`, `harness`, `directory`, `title`,
`state` (H1–H3: conservatively `unknown`; from H4-A per "Session state and the
Bridge guard"). Execution fields: `execution_id`, `session_id`,
`message_id`, `state`, `started_at`, `updated_at`, optional `error`, `ended_at`.
Native messages preserve `{info,parts}` including `sessionID`, `parentID`,
`time.completed`, `finish`, `error`, `path.cwd`. Parts and native errors are
unconstrained JSON, not base64 strings or invented normalized messages. Empty
history/result collections are arrays. State values and terminal proof retain
all H1 semantics; idle/HTTP 204 never prove successful completion.

Every tool's output schema is an object with `anyOf` success and typed failure
schemas. Domain/upstream failures have `isError:true` and structuredContent
`{code,message,retryable,execution_id?}`. Successes and domain failures also have
a JSON text content fallback generated by the SDK. Error bodies never copy
upstream response text. Invalid tool arguments are rejected by SDK schema
validation with `isError:true` and diagnostic text; these SDK-generated errors
do not promise a domain error envelope or structuredContent. Unknown tools and
malformed protocol requests use SDK JSON-RPC/HTTP errors. Authentication failures
are HTTP 401 before any SDK handling; missing server token is HTTP 500.

Transport: bearer-authenticated HTTP `/mcp` on loopback, MCP 2025-11-25 with
initialize/initialized, stateful SDK sessions, JSON responses, 1 MiB request body
limit, one JSON value per POST, one-minute idle transport-session expiry. Every
request including GET, DELETE and cancellation notifications needs the bearer.
An MCP session ID alone grants no authority. Origin and DNS rebinding checks are
enabled. HTTP DELETE/caller cancellation/transport expiry do not interrupt an
accepted native execution. SDK cancellation before acceptance cancels upstream
preflight; after acceptance use `interrupt_execution` explicitly. In a lost
response, reconcile using the same key and prompt; never blindly send a new key.

Reproduction: `go test ./internal/mcp -run TestSDK -count=1 -v` for protocol and
fixture integration, `go run ./cmd/mcpsmoke` with README's explicit existing-session
configuration for a live test. Exact quality outcomes and live limits are in
`H2_EVIDENCE.md`.

## H3 remote HTTP contract

The seven tool contracts and native lifecycle semantics are unchanged. Optional
remote mode requires HTTPS, exact configured Host, matching or absent Origin,
no query string, a single Authorization header and an unexpired credential.
401 includes a Bearer challenge; boundary denials use 403, invalid query/path 400,
and oversized POST 413. The principal is configured with its immutable ACL,
never supplied by request headers. This is fixed-header auth, not OAuth.

Token lifetime (ADR-019 option (2), accepted 2026-11-06):
- `BRIDGE_TOKEN_EXPIRES` (RFC3339) must be in the future and at most
  `BRIDGE_TOKEN_MAX_LIFETIME` from startup.
- `BRIDGE_TOKEN_MAX_LIFETIME` is a Go duration (for example `24h`, `168h`).
  Unset or empty means `24h`, the previous fixed cap, with unchanged behavior
  and error text. The compiled-in ceiling is `720h`.
- A value that is unparsable, `<= 0` or above `720h` fails startup in every mode,
  including local mode.
- `BRIDGE_TOKEN` must be at least 32 characters up to `24h`, and at least 43
  characters (about 256 bits as base64url) above `24h`.
- Remote mode logs one startup line, `remote token lifetime`, with the
  principal, `token_expires` and `token_max_lifetime`. It never logs the token or
  any part or hash of it.
- Expiry is still enforced on every request (`401`,
  `error="invalid_token"`), and the expiry or lifetime cannot change without a
  Bridge restart.
- Startup error order (fixed 2026-11-08, #102 nit 1): the token and the ingress
  are checked before any other secret is read or a native client is built. The
  first error reported is, in this order: `BRIDGE_TOKEN` / `BRIDGE_TOKEN_FILE`
  access; a token shorter than 32 characters, a missing token included
  (`BRIDGE_TOKEN must be at least 32 characters`, as before ADR-019); the
  ingress settings (`BRIDGE_READ_ONLY`, `BRIDGE_TOKEN_MAX_LIFETIME`, remote
  mode); the 43-character rule above `24h`; then `OPENCODE_SERVER_PASSWORD`
  and the remaining settings.
- The expiry bound is inclusive and read against one clock sample: an expiry
  exactly `BRIDGE_TOKEN_MAX_LIFETIME` after startup is accepted, a later one is
  rejected, and an expiry at or before startup is rejected.


Optional `BRIDGE_READ_ONLY=1` preserves the seven tools but returns FORBIDDEN for
send_prompt and interrupt_execution before native mutation. Default/0 preserves
existing write behavior; invalid values fail startup. It is required for the
separate staged private listener while the local Bridge remains a possible
writer; independent processes do not share execution/idempotency state.

## H3 single-writer admission and replay

Same session/key with a different prompt returns `IDEMPOTENCY_CONFLICT`,
`retryable:false` (previously INVALID_INPUT). Identical concurrent requests wait
through admission and return the same execution/message IDs once accepted.
Different keys on an active/uncertain session return SESSION_BUSY (for a `claudecode:` execution, ADR-021, unwired, a refusal for child capacity is also `SESSION_BUSY` with `retryable: true`; its code is a planner decision at wiring). Preflight
rejection is not acceptance; it may be retried after resolving native state.
All seven tool shapes remain unchanged. Caller cancellation while waiting for
admission does not cancel an independently accepted execution. Explicit
interrupt_execution remains authenticated/session-authorized and is forbidden
on read-only instances. See ADR-010 for required host ownership configuration,
nonparticipating native clients and lost restart state. Never interpret a fresh
process's NOT_FOUND as proof that an earlier prompt was not submitted.

## Mutation audit log (AUDIT-01)

Each `send_prompt` and `interrupt_execution` call writes exactly one structured
`log/slog` line for its outcome, on every return path (validation failure,
read-only denial, authorization failure, manager rejection, acceptance, replay).
Calls that the MCP SDK rejects against the tool input schema (for example a
`session_id` not matching its pattern, an `idempotency_key` shorter than 8, an
empty `prompt` or `execution_id`) never reach the tool handler; SDK receiving
middleware (go-sdk v1.8.0 `Server.AddReceivingMiddleware` on `tools/call`) writes
their single line with `outcome=rejected` and `error_code=INVALID_INPUT`. The
client receives the SDK's untyped tool error (`isError: true`, text
`validating "arguments": …`) rather than a structured `INVALID_INPUT` envelope;
the audit code names that rejection class. From raw arguments only string
`session_id`/`execution_id` values that pass the format checks are logged, and
only a string `idempotency_key` is hashed. Read tools write no audit line. The
message strings and the `accepted` key of the previous lines are kept for
existing journal searches, but `prompt submission accepted=false` now also
counts authorization, argument-validation and schema rejections, which earlier
builds did not log at all (read-only denials keep their own message), so counts
from older journal queries are not directly comparable.

| Key | Values | Notes |
|---|---|---|
| msg | `prompt submission`, `explicit interrupt`, `read-only mutation denied` | read-only denial replaces the other message for that call |
| event | `prompt_submission`, `interrupt` | |
| tool | `send_prompt`, `interrupt_execution` | |
| outcome | prompt: `new`, `replay`, `rejected`; interrupt: `accepted`, `rejected` | `replay` is the execution manager's decision (`SendWithRunnerOutcome`): same session, key and payload returned the existing accepted execution, including callers that waited on a concurrent identical admission |
| accepted | `true`, `false` | `true` exactly when outcome is not `rejected` |
| error_code | typed MCP error code | only when rejected, e.g. `SESSION_BUSY`, `FORBIDDEN`, `IDEMPOTENCY_CONFLICT`, `NOT_FOUND`, `INVALID_INPUT`, `UNKNOWN_OUTCOME`, `UPSTREAM_FAILURE`; `INTERNAL` if the call panicked |
| session_id | `opencode:ses_…` | omitted when the input was not a well-formed session ID; for interrupt, taken from the execution once found |
| execution_id | 32 lowercase hex | omitted when unknown; a client-supplied value is logged only if it has the manager's format |
| idem | 12 lowercase hex | prompt only: first 12 hex characters of sha256(idempotency_key); never the raw key |
| principal | configured remote principal, or `local` | `local` for loopback-token mode, which has no named principal |

Never logged: prompt text, history, result text, tokens, headers, raw
idempotency keys or other secrets. Synthetic examples (default handler, timestamp prefix omitted):

```
INFO prompt submission event=prompt_submission tool=send_prompt outcome=new accepted=true session_id=opencode:ses_example execution_id=0f1e2d3c4b5a69788796a5b4c3d2e1f0 idem=06a48394d463 principal=operator
INFO prompt submission event=prompt_submission tool=send_prompt outcome=replay accepted=true session_id=opencode:ses_example execution_id=0f1e2d3c4b5a69788796a5b4c3d2e1f0 idem=06a48394d463 principal=operator
INFO prompt submission event=prompt_submission tool=send_prompt outcome=rejected accepted=false error_code=SESSION_BUSY session_id=opencode:ses_example idem=9c1e4b7a52d0 principal=operator
INFO explicit interrupt event=interrupt tool=interrupt_execution outcome=accepted accepted=true session_id=opencode:ses_example execution_id=0f1e2d3c4b5a69788796a5b4c3d2e1f0 principal=operator
INFO read-only mutation denied event=prompt_submission tool=send_prompt outcome=rejected accepted=false error_code=FORBIDDEN session_id=opencode:ses_example idem=3a7d0c1f9e44 principal=operator
```

Limits: `new` means the manager admitted a new execution, not that the native
harness accepted or completed it; use execution status/result. Audit state is
per Bridge process, like idempotency state (ADR-010). `idem` is a 48-bit
correlator, not a secrecy guarantee: a low-entropy key can be guessed from it, so
clients should use high-entropy keys. Lines are written before the MCP response
is returned; a crash between decision and log write loses the line.

The SDK has a second pre-handler rejection path: arguments that pass the input
schema but fail to unmarshal into the tool's argument type (go-sdk v1.8.0
`mcp/server.go:412-415`) return the raw decoder error text as the untyped tool
error, and the audit records them the same way, as one line with
`outcome=rejected` and `error_code=INVALID_INPUT`. Requests whose JSON-RPC params
cannot be decoded at all never reach the audit middleware (the tool name is
unknown), so they write no audit line; this path is untested.

## H3 late-completion contract (ADR-011)

This section supersedes the old frozen-snapshot behavior after monitoring ends.
Tools and input schemas remain unchanged. Only an existing in-memory execution
can be reconciled; there is no arbitrary message-ID recovery/import endpoint.

Optional additive `Execution` fields:

| Field | Meaning |
| --- | --- |
| `unresolved.reason` | `MONITOR_EXPIRED`, `SUBMISSION_UNCONFIRMED`, `OBSERVATION_UNAVAILABLE`, `ABORT_UNCONFIRMED`, `IDENTITY_MISMATCH` or `MANAGER_CLOSED`. For a `claudecode:` execution (ADR-021 §3, unwired) three more additive values: `EXIT_UNCONFIRMED` (a result was seen but the child did not exit or its exit could not be confirmed), `PROCESS_ENDED_WITHOUT_RESULT` and `UNKNOWN_OUTCOME` (no named reason: rows 7, 8, 10). The `child_unconfirmed` flag (additive, `claudecode:` only) accompanies the `unknown` verdicts of ADR-021 §3 rows 0, 9, 6a and 6b, the rows whose child may still be alive when the verdict is decided; rows 1s and 9s do not set it (a stdin failure and a Bridge shutdown carry their own reasons, and the guard stays held). It is audit information: the guard release below tests the child's present state, not the flag. |
| `unresolved.since` | UTC time the currently retained uncertainty cause began |
| `unresolved.last_checked_at` | Most recent on-demand evidence attempt, including inconclusive/unavailable reads |
| `monitor_ended_at` | UTC time the bounded background worker exited; never native completion proof |
| `reconciled_at` | UTC time terminal evidence was committed by an on-demand read or explicit-interrupt evidence check |

`unknown` is nonterminal; `ended_at` remains absent without proof. Background
expiry preserves IDs, idempotency and busy guard. Subsequent status/result reads
may promote the SAME execution to completed/failed/interrupted after original
session/workspace/user/assistant-parent/positive-completed-time/final-finish-or-
named-error proof, no correlated unfinished assistant and no pending permission.
A missing native status is acceptable ONLY with that full terminal evidence.
Pending permission remains waiting_for_permission and cannot release the guard.

While the background monitor runs and there is no terminal proof, native
status `busy` or `retry` reports `running`. An absent entry, an explicit `idle`
entry or an unsupported status reports `unknown` with `unresolved.reason =
OBSERVATION_UNAVAILABLE`; explicit `idle` is treated exactly like absent and is
never reported as `running` (#86 finding 3, changed 2026-11-08; earlier builds
reported `running` for an explicit `idle` entry). OpenCode 1.19.4 deletes idle
entries from `/session/status`, so it only ever shows the absent case.

The original unresolved reason/since are retained while late checks remain
inconclusive; the error string gives a sanitized current explanation. Identity
violations return typed FORBIDDEN, never native content. Metadata/permission
read failures cannot establish success or authorize an Abort. Once proven
terminal, clear unresolved and preserve the final snapshot without re-probing.
`ended_at` is now the native completion time, not the time Bridge happened to
observe it; `updated_at` / `reconciled_at` distinguish observation time. This
semantic clarification matters to clients measuring durations or detection lag.

Reconciliation happens AFTER current MCP session/root authorization and original
workspace validation. `Snapshot` is an internal local-only lookup, not a new MCP
tool. Status/result no longer necessarily have zero upstream latency: one probe
is bounded to 5s including global-slot/per-run-gate waits, or a shorter caller
deadline. MCP's preceding authorization metadata request retains the adapter's
10s bound. Concurrent reads coalesce; completed attempts have the poll-interval
cooldown (default 500ms). At most 4 on-demand probes run per manager; at most one
native observation/Abort operation runs for each execution. Queue/request timeout
may return a tool error; it neither releases the guard nor retries Prompt.

The background monitor remains 300s/default 500ms. No perpetual reconciliation loop
is created: another bounded attempt requires another read call. Cancelled client
reads cannot cancel accepted native work; Close cancels/joins monitors/probes.
Explicit interruption still requires the authenticated authorized tool, preserves
abort-acknowledgement semantics, and refuses already proven terminal executions.
No automatic permission/interrupt action is added. Native compare-and-abort is
not atomic against nonparticipating direct native clients.

Restart still loses IDs/keys/results; NOT_FOUND after restart is not proof of
non-delivery or successful completion. Reconcile native history using the private
operator ledger before any writer restart or new prompt, as ADR-010/ADR-011 specify.

## ADR-017 report-only liveness (option (a), accepted 2026-11-05)

`get_execution_status`, `get_execution_result` and the replay response of
`send_prompt` may carry an optional, additive `liveness` object on the
execution. Tools and input schemas are unchanged; clients that ignore unknown
fields are unaffected (the execution schema already allows additional
properties). The object is **advisory only**: it never changes `state`,
`error`, `unresolved`, `ended_at` or `updated_at`, never releases the session
guard, never triggers Abort, a resubmission or a new idempotency key, and
`stall_suspected` is never a terminal state and never success.

| Field | Meaning |
| --- | --- |
| `liveness.native_status` | `busy`, `retry`, `absent` (no native status entry, or an explicit `idle` entry, which OpenCode 1.19.4 does not keep) or `unavailable` (this probe's native reads did not all succeed, the observation failed identity checks, or the native type is unrecognized) |
| `liveness.retry_attempt` | Native retry attempt; only with `retry` and a well-formed non-negative integer |
| `liveness.retry_next_at` | Native next retry time (from epoch ms); only with `retry` and a well-formed value |
| `liveness.last_native_progress_at` | Latest native timestamp among the correlated assistant messages' `time.created`/`time.completed` and recognized parts (text/reasoning `time.start/end`, tool `state.time.start/end`); never after `observed_at`. Unknown, malformed or over-1-MiB parts are ignored |
| `liveness.stall_suspected` | `true` only with a `stall_reason`; always present in the object |
| `liveness.stall_reason` | `COMPLETED_WITHOUT_TERMINAL_EVIDENCE` (correlated assistant completed, no `finish` stop/end_turn or named error, native status absent, no pending permission), `NO_CORRELATED_ASSISTANT` (user message present, no correlated assistant, status absent), `NO_PROGRESS` (`busy`, or `retry` whose `next` is past, and no native progress, nor submission, for longer than 2 minutes) or `HARNESS_UNREACHABLE` (the accepted A2 native-read failure class; the S3 slice introduces it and S1 does not emit it yet) |
| `liveness.degradation_reason` | ADR-017 A2 Variant A: `PROVIDER_RETRYING` (native status `retry`, including a healthy retry), `PROVIDER_RETRY_OVERDUE` (the same but `retry_overdue` is true, D32 Q11) or `PROVIDER_ERROR` (an unresolved execution whose native status is not `retry` and that has a typed provider error: a correlated assistant error without terminal proof, or a persisted `retry` part). Independent of `stall_suspected`; a healthy retry is degradation, never a stall |
| `liveness.degraded` | Only the alias: `true` iff `degradation_reason` is present; never a separate state |
| `liveness.retry_overdue` | `true` only with `retry` when `next` is past by more than the larger of 30 s and the NO_PROGRESS threshold, and the current attempt has been observed for at least that threshold. An additional hint: a wedged `NO_PROGRESS` may or may not carry it |
| `liveness.cause_hint` | One closed word: `network`, `rate_limit`, `auth`, `server` or `unknown`. From the structured source (`action.reason`, an `APIError` status code, `ProviderAuthError` or a `retry` part) or the D32 Q5 bounded-text fallback; never the raw text |
| `liveness.cause_source` | `structured` (a structured field, even when it maps to `unknown`), `bounded_text` (only from the closed pattern set) or `none`. The matched text is never authority, never exported and never in audit |
| `liveness.observed_at` | Bridge time of the probe that produced the object |

ADR-017 A2 also adds `retry_attempt_since` to the execution record (not inside the
`liveness` block): the Bridge time of the first observation of the current retry
attempt. It is report-only memory for `retry_overdue`, cleared when the attempt
number changes, the retry ends or the status is no longer `retry`; like every
execution field it is process-local.

The A2 provider fields are additive and optional, follow the same report-only
rules (they never change `state`, `ended_at`, `unresolved`, the guard or
idempotency), and a pending native permission suppresses only the stall reason,
not the provider degradation dimension. A retry whose `next` is past with no
progress for longer than the threshold keeps A1's `NO_PROGRESS`/`stall_suspected`,
with a running tool or not; `degradation_reason` accompanies it. The bounded
fallback lowercases at most 4 KiB of the retry message and maps only the closed
pattern set (429/rate limit, 5xx/overloaded, DNS/TLS/connection, 401/403,
timeout); a text matching no pattern stays `unknown` with `cause_source: none`.
Pattern groups resolve in the order `rate_limit` > `auth` > `server` > `network`
(the first matching group wins); a textual `timeout` maps to `network`, while a
structured HTTP 408 still maps to `server` (A2.5).
It is report-only and adds no native read or write. The accepted A2 amendment
adds `HARNESS_UNREACHABLE` as the only new `stall_suspected`-true `stall_reason`;
the S3 slice introduces it, so this release does not emit it yet.
`TOOL_RUNNING_LONG` is not a `stall_reason` (it is the S2 explanation) and is not
emitted yet either.

The object is computed from the native reads the background monitor and the
bounded on-demand reconcile already make (metadata, status, history,
permissions); no native read or write is added. It is refreshed by each such
probe, so between probes it may be up to one probe interval old (see
`observed_at`). A pending native permission never produces suspicion. It is
absent before the prompt is submitted and on terminal executions. Like all
execution records it is process-local: a restart loses it with the execution
(NOT_FOUND), and old idempotency keys must still never be reused. The NO_PROGRESS
threshold (2 minutes) is fixed in this release, so a long native step without
part updates can be reported as suspected. Native timestamps come from the
OpenCode clock and are compared with the Bridge clock: a native timestamp later
than `observed_at` is ignored, so if the OpenCode clock runs ahead of the Bridge
clock, real progress can be ignored and a false `NO_PROGRESS` suspicion can appear
(advisory only, like every suspicion). Recovery from a suspected stall stays
the ADR-010 fenced procedure; ADR-017 §4 lists what the R5 supervisor records,
and the supervisor may surface suspicion to a human but must not act on it.

## ADR-018 report-only permission rejection (option (d) on (a), accepted 2026-11-06)

`get_execution_status`, `get_execution_result` and the replay response of
`send_prompt` may carry an optional, additive `permission_rejection` object on
the execution. Tools and input schemas are unchanged. The object is
**report-only and never proof of an outcome**: it never changes `state`,
`error`, `unresolved`, `ended_at` or `updated_at`, never releases the session
guard, never triggers Abort, a native permission reply, a resubmission or a new
idempotency key. **The supervisor must not act on it automatically**; it may
show it to a human. After a native rejection the execution stays as option (a)
decides (`unknown`, guard held; ADR-010 fenced recovery).

| Field | Meaning |
| --- | --- |
| `permission_rejection.observed_at` | Bridge time of the probe that produced the object |
| `permission_rejection.tool` | Native tool name of the rejected tool part (`[A-Za-z0-9_.:-]{1,64}`) |
| `permission_rejection.tool_call_id` | Native `callID` of that tool part (`[A-Za-z0-9_.:-]{1,128}`) |
| `permission_rejection.basis` | `native_event` or `persisted_text_only` (below) |

The object is present only when the persisted native evidence matches
ADR-018 §3(b) items 2-3, all of which must hold:
- the latest correlated assistant message is completed, has `finish:
  "tool-calls"` and no error;
- no other correlated assistant is as late, completed later or unfinished;
- it has exactly one tool part with `state.status: "error"`, whose
  `state.error` is byte-equal to OpenCode's `RejectedError` text ("The user
  rejected permission to use this specific tool call."). A `CorrectedError`
  text never matches;
- no tool part is `pending` or `running`.

It is absent when any of these fails or is unparseable, while a native
permission is pending (`waiting_for_permission`), when the probe's reads fail,
and on terminal executions.

- **`basis: "native_event"`**: additionally, this Bridge process's in-memory
  permission observer saw the request asked with a verified binding to this
  execution and resolved by a native `reject` reply for the same assistant
  message and tool call, with no gap, fence, conflict or unattributed state
  (§3(b) item 4). It is still not a terminal state and still not proof that a
  human rejected this request: a cascade-rejected request also gets a `reject`
  reply.
- **`basis: "persisted_text_only"`: untrusted.** Only the persisted message
  shape matched. The same text can come from instance disposal, a cascade
  reject, or any tool, plugin or MCP server error with that message.

**Current limitation:** the permission observer is not wired into the running
Bridge, so production reports only `persisted_text_only`.

The object is computed from the native reads the monitor and the bounded
on-demand reconcile already make, plus the observer's memory; no native read or
write is added. Like all execution records it is process-local: a restart loses
it with the execution. Pending-permission detail (ADR-018 §5) is PROPOSED and
not implemented.

## ADR-020 operator guard release (option (b), accepted 2026-11-08)

A guard stuck on `unknown` can be released for **one** session by the operator,
on the host, without a Bridge restart. This is **not** an MCP tool: the eleven
tool names and their schemas are unchanged, there is no TCP or HTTP listener for
it, and the remote connector cannot reach it. The Chat principal can never
release a guard.

**Admin socket.** Set `BRIDGE_ADMIN_SOCKET` to an absolute path. The writer
opens it after it acquires writer ownership (ADR-010); read-only ingress never
opens it (the variable is ignored, with one log line). Startup fails unless the
parent directory is owned by the service user with mode exactly `0700`. A stale
socket at the path is replaced only if it is a socket owned by the service user;
any other file there fails startup. The socket is created with `net.Listen` and
only then set to mode `0600`; for that short window it has the process umask's
mode. The real fence is the `0700` parent directory owned by the service user:
nobody else can reach the socket path at all, so the `0600` mode is a second
layer, not the primary one.
Each connection must come from the service user's UID (`SO_PEERCRED`); root uses
`sudo -u <service user>`. One newline-delimited JSON request per connection, at
most 4096 bytes **including the terminating newline** (a line of 4095 bytes
plus the LF is accepted, 4096 bytes plus the LF is refused as `request too
large`; pinned by `TestRequestLimitCountsTrailingNewline`), read within 5 s; the
connection is closed after 60 s. At most 4 connections are served at once: a
fifth is refused at once, without reading from it, with the retryable
`TOO_MANY_CONNECTIONS` (fail-closed; nothing is queued and no release is
attempted). Unknown JSON fields are refused, so no free-text reason can be sent.
Unset variable: no socket, no release path.

**Trust boundary.** The client (`bridge admin release-guard`) does not check who
owns the server end of the socket; it relies on the `0700` directory. A
same-UID attacker (another process running as the service user) is out of scope:
it can already read the service's secrets and drive the Bridge directly. Two
consequences of the same boundary are accepted, not defended: such a peer can
hold all 4 slots (each until the 5 s read bound) and reopen them indefinitely,
so the operator can be locked out of the admin socket while it does; and every
refusal writes one audit line, so a flood of connections grows the journal at
the rate of connections. The cap is checked before the UID check, so a peer of
another UID that could reach the socket (the `0700` directory prevents that)
would be answered `TOO_MANY_CONNECTIONS` rather than `FORBIDDEN` while the
server is full; either way nothing is read from it and no release is attempted.

**CLI** (same binary, runs as a client and needs none of the server's secrets):

```
sudo -u <service user> env BRIDGE_ADMIN_SOCKET=<path> \
  bridge admin release-guard --session opencode:ses_<id>|claudecode:<uuid> --execution <execution_id> --ticket <ticket> [--allow-missing-user-message]
```

`--socket <path>` overrides `BRIDGE_ADMIN_SOCKET`. `--ticket` is required and
must match `[A-Za-z0-9._-]{1,64}`. The command prints the JSON response and
exits `0` when released, `1` when refused or the socket is unreachable, `2` on
a usage error (also for a bad ticket, checked before connecting).

**Claude Code sessions (ADR-021 slice 4, D14 Q8; defined, not served).** The same operation
accepts `--session claudecode:<uuid>` (canonical lowercase UUID). Its preconditions replace the
native reads, which do not exist for this harness, and all are checked by the Bridge: (1) the
guard of the session is held by exactly this execution; (2) the execution is `unknown` with a
decided verdict; (3) its child is **confirmed exited**: the run's `Wait` returned a known
status (an unknown `Wait` confirms nothing), the run's pipeline has ended and the child's
process group has no member left; (4) after the settle interval (default 5 s) the same facts
hold again, one re-check in place of the two reads. Any unknown fails closed with a typed
error: `INVALID_INPUT` (not the holder, not `unknown`), `SESSION_BUSY` (child not confirmed
exited, group not empty), `UPSTREAM_FAILURE` (group check failed), `UPSTREAM_TIMEOUT`
(cancelled during the settle) or `UNKNOWN_OUTCOME` (manager closed). The release never changes
the execution's state, reason, verdict or history and sends no signal (no SIGKILL exists
anywhere). `--allow-missing-user-message` does not apply and is refused. While no Claude Code
Executor is constructed in production (it is not), every `claudecode:` request is refused with
`INVALID_INPUT` "claudecode sessions are not enabled" and one audit line. Response: `outcome`,
`session_id`, `execution_id`, `claude_evidence` (`harness`, `native_read: false`, `row`,
`exit_class`, `child_exited`, `group_empty`, `rechecked`, `settle_ms`, `guard_released_at`) and
`error`. The one audit line carries `harness=claudecode`, `child_exited`, `group_empty`,
`rechecked`, `row`, `exit_class`, `settle_ms`, `ticket`, `principal`, and none of the
native-read keys.

Response:

| Field | Meaning |
| --- | --- |
| `outcome` | `released` or `refused` |
| `session_id`, `execution_id` | echoed only when well formed |
| `evidence.native_read` | whether a native read happened |
| `evidence.native_state` | `absent`, `idle`, `busy`, `retry`, `unknown` (last read) |
| `evidence.unfinished`, `evidence.permission_pending` | from the last read |
| `evidence.correlated_user` | `found` or `absent` |
| `evidence.settle_ms` | settle interval used (5000) |
| `evidence.guard_released_at` | Bridge time of the release (released only) |
| `error` | the typed error (`code`, `message`, `retryable`) when refused |

**Preconditions, all checked by the Bridge** (the operator's word is not
enough):
1. the guard of the session is held by exactly this execution (compare-and-swap);
2. the execution is `unknown` and its background monitor has ended;
3. a fresh native read, holding the execution's `x.op` token: identity and
   workspace check before and after the reads, native status absent or `idle`,
   no unfinished assistant anywhere in the full history (F-5 predicate), no
   pending permission for the session;
4. the correlated user message exists, or `--allow-missing-user-message` was
   given (the lost-Prompt case; audited);
5. after a 5 s settle interval, during which `x.op` is **not** held (status
   polls and interrupts proceed normally), checks 1-2 are repeated, then a
   second read repeats check 3-4 and must show the same message IDs and the same
   status;
6. a final compare-and-swap and state check under the run and manager locks,
   still holding `x.op`, then the guard is deleted.

**Failure codes:**

| Code | When |
| --- | --- |
| `INVALID_INPUT` | malformed or oversized request, unknown field, bad ticket, bad session or execution ID, guard not held by this execution (including a newer execution holding it), execution not `unknown` or monitor still running, correlated user message absent without the override |
| `SESSION_BUSY` | native status `busy`, `retry` or unsupported; unfinished assistant; pending permission; native evidence changed during the settle interval |
| `FORBIDDEN` | peer is not the service UID; session identity or workspace changed |
| `UPSTREAM_TIMEOUT` | a native read or workspace check did not complete within the 5 s bound (retryable); also when the request's context is cancelled or expires during the 5 s settle sleep (the caller went away or the 60 s connection bound passed; nothing is decided, the guard is kept, `evidence.settle_ms` is set because the failure is after the first read) |
| `TOO_MANY_CONNECTIONS` | more than 4 admin connections at once; refused before any read (retryable, admin socket only) |
| `UPSTREAM_FAILURE` | a native read failed (retryable) |
| `UNKNOWN_OUTCOME` | the manager is shutting down |

Every refusal leaves the guard and the execution unchanged.

**Effect.** Only the guard is deleted and the additive execution field
`guard_released_at` is set. `state`, `error`, `unresolved`, `ended_at`,
`updated_at`, idempotency keys, fingerprints and the ADR-010 lock files are
unchanged. The old key still replays the same `unknown` execution and never
submits again; a different payload with it still conflicts. A new key goes
through the normal admission preflight. ADR-011 promotion of the old execution
remains possible; its guard delete is conditional on the guard still naming that
execution, so it never removes a newer execution's guard.

**Audit.** Exactly one AUDIT-01 line per request, on every return path,
including refused peers and malformed requests:

| Key | Values | Notes |
| --- | --- | --- |
| msg | `guard release` | |
| event | `guard_release` | |
| tool | `admin` | never an MCP tool name |
| outcome | `released`, `refused` | |
| accepted | `true`, `false` | `true` exactly for `released` |
| error_code | typed code above | refused only |
| session_id, execution_id | as in AUDIT-01 | only when well formed |
| native_state, unfinished, permission_pending, correlated_user | as in the response | only when a native read happened |
| settle_ms | integer | only when the settle interval was used |
| allow_missing_user_message | `true` | only when the override was requested |
| ticket | `[A-Za-z0-9._-]{1,64}` | only when valid; an invalid ticket is refused and never logged |
| principal | `admin:uid=<n>` | peer UID from `SO_PEERCRED`; `admin:uid=unknown` if it could not be read |

No free text, no prompt or history content. Synthetic examples:

```
INFO guard release event=guard_release tool=admin outcome=released accepted=true session_id=opencode:ses_example execution_id=0f1e2d3c4b5a69788796a5b4c3d2e1f0 native_state=absent unfinished=false permission_pending=false correlated_user=found settle_ms=5000 ticket=OPS-1234 principal=admin:uid=998
INFO guard release event=guard_release tool=admin outcome=refused accepted=false error_code=SESSION_BUSY session_id=opencode:ses_example execution_id=0f1e2d3c4b5a69788796a5b4c3d2e1f0 native_state=busy unfinished=false permission_pending=false correlated_user=found ticket=OPS-1234 principal=admin:uid=998
```

**Limits.** The release cannot prove what happened to the original prompt; it
makes the operator's decision explicit, local, audited and per session. The
override leaves a residual double-submission risk if a lost Prompt arrives late.
The settle interval is a heuristic, not a native lease. Like all execution
records, `guard_released_at` is process-local. Token rotation (ADR-019) still
needs a restart. Live acceptance (ADR-020 §4) is not yet recorded.

## ADR-021 Claude Code harness contract (served only when enabled; default off)

Accepted ADR-021 (D12/D14), wired by the wiring slice **behind one explicit operator
configuration**. With no `BRIDGE_CLAUDE_COMMAND` (the default) nothing here is served: a
`claudecode:` session ID is refused `INVALID_INPUT` "not enabled" for every tool, the 11-tool set,
every OpenCode response and every OpenCode audit line are byte-identical to a build without the
adapter, and `bridge admin release-guard` refuses `claudecode:` IDs. With it, the Bridge builds the
adapter at startup or refuses to start (no partial enablement; see "Enabling" below). A read-only
ingress never enables it. Tool names, input fields and output fields of the eleven base tools (twelve with `reply_permission` under the
default `reject_only` relay) do not change.

**Enabling.** `BRIDGE_CLAUDE_COMMAND` (absolute path of the root-owned wrapper) turns the adapter on and
then requires `BRIDGE_CLAUDE_SESSIONS` (allowlist file, mode 0600), `BRIDGE_CLAUDE_VERSIONS` (exact
`claude_code_version` list, no default), `BRIDGE_CLAUDE_HISTORY_DIR`, `BRIDGE_CLAUDE_HOME` (the Claude
service user's HOME for the child), `BRIDGE_CLAUDE_MODEL` and the existing `BRIDGE_WRITER_LOCK_DIR`;
optional: `BRIDGE_CLAUDE_LANG`, `BRIDGE_CLAUDE_MAX_TURNS` (1..1000, default 20),
`BRIDGE_CLAUDE_MAX_BUDGET_USD`, `BRIDGE_CLAUDE_RUN_MAX` (1s..24h, default 15m). Every `claudecode:` session
must be in the allowlist file **and** in `BRIDGE_ALLOWED_SESSIONS`. Any other `BRIDGE_CLAUDE_*` value without
the command, or an unusable value with it, stops the start with an error. The writer lock of a
`claudecode:` session is taken once, by the adapter; the OpenCode writer takes only the other IDs.

**Routing (exact ID only, no discovery beyond the allowlist).** `list_sessions` and `find_sessions` list the
allowlisted, ACL-authorized Claude sessions with the OpenCode ones (sorted by session ID; a Claude session
carries no repository, workspace or parent, so those filters never match it); `get_session`,
`read_session_history`, `send_prompt` and `get_harness_capabilities` take a `claudecode:` ID;
`get_execution_status`, `get_execution_result` and `interrupt_execution` route by execution ownership. An ID
outside the allowlist is `NOT_FOUND` (no child is started), outside the ACL `FORBIDDEN`; the execution tools re-check the execution's session the same way. `list_catalog`
(ADR-022) still omits Claude sessions. `get_session` state: `running` while a child runs, `unknown` while an
unknown or unreaped execution holds the guard, otherwise `idle` (only "the Bridge holds no guard and runs
no child"; weaker than OpenCode's `idle`).

- **Session ID.** `claudecode:<lowercase 8-4-4-4-12 hex UUID>` in the same `session_id` field
  (an `anyOf` of the two patterns, see "Input JSON Schema"). Claude sessions are advertised to
  all clients (D14 Q2). A strict old client that validates against the OpenCode pattern only
  would reject the new form; the contract version is bumped when it is served.
- **Session fields.** `harness: "claudecode"`, `native_id` = the UUID, `title` = the allowlist
  label, `directory` = the root, `state` per ADR-021 §6.
- **Execution fields.** `session_id` is `claudecode:<uuid>`; `message_id` is the Bridge's own
  opaque execution-scoped ID (the CLI exposes no user message ID); additive optional
  `permission_denials`, `liveness` (not served yet), `child_unconfirmed` (rows 0, 9, 6a, 6b only, see
  `unresolved.reason` above), `cost_usd_reported` (informational, never used for correctness)
  and `exit` (`class` only: `zero`, `nonzero`, `signal`, `unknown`). `permission_denials` is a list of
  `{tool_name, input}` (a bounded, redacted summary). **`liveness` is defined but not served yet.**
- **Unresolved reasons.** Existing: `MONITOR_EXPIRED`, `SUBMISSION_UNCONFIRMED`,
  `ABORT_UNCONFIRMED`, `IDENTITY_MISMATCH`, `MANAGER_CLOSED`. Additive for this harness:
  `EXIT_UNCONFIRMED`, `PROCESS_ENDED_WITHOUT_RESULT`, `UNKNOWN_OUTCOME` (no named reason).
- **Errors.** Unchanged enum. A history store or size refusal is `UPSTREAM_FAILURE`
  (`retryable: false`); there is no `INTERNAL`. A child-capacity refusal is `SESSION_BUSY` with
  `retryable: true` (its code stays a planner decision at wiring).
- **`read_session_history`.** Bridge-normalized messages (each record becomes one message whose `role` is
  `user` for a turn start, `assistant` for an assistant block, `tool` for a tool result and `system` for the
  rest, with one part: the record's API projection) plus the additive `source: "bridge_record"`,
  `history_complete`, `first_recorded_at`, `history_truncated`; `limit` returns the newest N, a `before`
  cursor is refused `INVALID_INPUT`; the 8 MiB response bound is unchanged. History exists only for turns the
  Bridge ran (ADR-021 §5). `get_execution_result` returns the execution and its own turn's messages.
- **`get_harness_capabilities` (ADR-012) block for Claude.** `harness: "claudecode"`,
  `version` (the version last seen in `init`), `capabilities` (the init tool list, verbatim,
  informational) and Bridge facts, each "declared by the Bridge", not probed from the CLI:
  `resume_exact_id: true`, `native_lock: false`, `history_source: "bridge_record"`,
  `permission_relay: false`, `interrupt: "sigint_then_sigterm"`, `active_terminal_control: false`,
  `session_creation: false`, `read_only_tools: true`.
- **Interrupt.** `interrupt_execution` keeps its contract. For Claude it is SIGINT, a bounded wait
  (default 10 s), SIGTERM, a bounded wait (default 5 s), then the Bridge stops signalling and the
  execution is `unknown` with `child_unconfirmed`; there is no SIGKILL, ever (removing a stuck
  process is an operator action).
- **Guard release.** Operator-only, through the admin channel (ADR-020 extension above). There is
  no automatic release for an `unknown` Claude execution. A **known wrapper exit does not free the guard
  while the child's process group still has a member** (the CLI behind `sudo` may still run): the run
  is `unknown` (row 6a with a result, row 10 without) and the operator release requires an empty group.
  An empty group does not prove that a `setsid`/detached descendant is gone.
- **Audit.** AUDIT-01 gains optional keys only (`harness`, `interrupt_reasons`,
  `late_lines_after_verdict`, `late_result_seen`, and the Claude release keys above); never
  prompt text, keys, tool input or line content.

## H4-A additive discovery contracts (not deployed)

- `list_repositories {}` -> `{repositories:[{repository_id,common_directory}]}`.
- `get_repository {repository_id}` -> one visible repository; otherwise NOT_FOUND.
- `find_sessions {repository_id?,workspace_id?,parent_session_id?,limit?,offset?}`
  -> `{sessions:[],next_offset?}`. Default 50, limit <=200, offset <=1000; session-ID
  order. Registry maximum 1000 ACL entries, 10s total deadline. Filters never grant
  authority and cannot accept a client directory. Results can change between reads.
- `get_harness_capabilities {session_id}` -> harness_instance_id, primary_agents,
  subagents, native_delegation (`unknown`), child_session_inspection and
  native_session_creation (installed endpoint declarations), bridge_session_creation
  (`false`), evidence and conditional guidance. This tool requires session ACL and
  workspace authorization before reading the native inventory/specification. For a
  workspace-bound session (non-empty `native_workspace_id`) it returns
  `INVALID_INPUT` (`retryable:false`, message "unavailable for workspace-bound
  sessions") without any `/agent` or `/doc` request (H4A-F2-FOLLOWUP).
- `read_session_history` retains session_id and messages; optional limit (1–200)
  and before (opaque native cursor, <=2048 URL-safe characters) enable bounded
  native pages; optional next_cursor comes from X-Next-Cursor. Omit both inputs
  for old behavior. No promised numeric total, snapshot consistency or synthetic
  cursor; an upstream server ignoring the limit fails closed.

### ADR-022 S3: `list_catalog` (read-only; registered only when configured)

`list_catalog {root?, state?, updated_since?, created_by_bridge?, harness_name?,
include_archived?, limit?, offset?}` -> `{harnesses:[], sessions:[{session_ref:
{harness:"harness:<hash>", session_id:"opencode:<id>"}, harness_name, directory, root?,
title, state, updated_at?, created_by_bridge, archived}], next_offset?, aggregates:
{scope:"page", window:{offset,limit,returned}, total, by_state, by_root}}`.

- Lists exactly the sessions the immutable ACL authorizes (same bounded discovery as
  `find_sessions`); it never lists a session outside the ACL and the lifecycle roots grant
  nothing here. Filters are exact matches; `state` is `idle|running|waiting_for_permission|
  unknown` (`archived` is refused); `updated_since` is RFC 3339 and a session with no native
  update time never matches. Limit default 50, 1-200; offset 0-1000; session-ID order; a
  fresh view, not a snapshot; one total deadline of 10 s.
- `session_ref` is a first step toward an instance-qualified ID (ADR-022 6.2), not the
  ADR-012 versioned ID; the other tools keep the `opencode:<id>` form. Aggregates describe the
  returned page, not the fleet. Claude Code sessions are not listed (`harnesses` names the
  kinds that are).
- Errors, never a short page: `INVALID_INPUT` for a bad filter (before any native read);
  `UPSTREAM_TIMEOUT` (retryable) when the deadline passes; the native error code when a native
  read of a listed session fails; `INTERNAL` when the archive overlay cannot answer.
- Read-only: no native write, not audited as a mutation, available under `BRIDGE_READ_ONLY`.
  Overlay-archived sessions (the persisted overlay of ADR-025, opened from `BRIDGE_OVERLAY_DIR` when it
  is set; otherwise none) are hidden unless `include_archived` is true. Every row carries `archived`
  (`false` for a session that is not archived; before the H5 live sitting the field was omitted when
  false, so a client could not tell "not archived" from "not supported").
- The tool is registered only when the server has a catalog configuration, which `cmd/bridge` builds when
  `BRIDGE_LIFECYCLE_CONFIG` is set and valid (D22, `cmd/bridge/lifecycle.go`); unset, a deployed Bridge
  lists the same tools as before. It stays registered on a read-only ingress (it never writes).

### ADR-022 S4: `create_session` (against a fake native; registered only when configured)

`create_session {root, subdirectory?, title?, idempotency_key}` ->
`{session_id, root, directory, title, created: true, replay?: true}`. `session_id` is `opencode:<id>`.
`replay` is present, and `true`, only on an answer that is a recorded outcome returned again for the same
idempotency key and input (H5 live finding): every other field is the recorded output verbatim, so
`created` stays `true` on a replay. A first answer never carries it. The
input schema has no other property: a `prompt`, `permission`, `agent`, `model`, `parentID`,
`workspaceID` or `harness` is rejected before the handler runs (the first prompt needs the O2
flag and slice S9, D16 Q7; only `opencode` exists until ADR-021 is wired).

- **Authority** is the lifecycle configuration plus the live directory (ADR-022 3.2), not the
  ACL: a created session is NOT in `BRIDGE_ALLOWED_SESSIONS` and the existing tools do not act on
  it (ADR-022 3.2 O1/O2/O3 is undecided; until S9 the operator adds the ID to the ACL).
- `root` must equal a configured root path exactly (no trailing slash, no alias). `subdirectory`
  is relative and clean, without `..`, empty, `.` or control components, must already exist
  without symlink components, and at most `max_depth` levels deep (absent = 0: the root itself).
  The Bridge never creates a directory. The fully resolved canonical path is what is sent
  (`POST /session?directory=<path>`, body `{title?, metadata: {hb_origin: "bridge", hb_key:
  <first 16 hex of sha256(key)>}}`; nothing else, never the raw key).
- **Refusal causes.** The cause of an authorization refusal is internal (audit line and tests) except
  one closed word: a `FORBIDDEN` refusal whose cause is `unavailable` (a filesystem check of the
  directory, its Git metadata or the lifecycle root could not complete, for example a permission problem
  of the service user) ends its message with `(unavailable: a filesystem check could not complete; check
  the service user's read access to the lifecycle root, the directory and its Git metadata)`. No path is
  ever in a message, and every other cause keeps the message it had (H5 live finding F-H5-2: a lifecycle
  root the service user could only search made every create `FORBIDDEN` with no hint).
- **Startup check of the roots.** Each lifecycle root is opened at startup the way the request path opens
  it (`os.OpenRoot`, which needs read and search permission); a root the service user cannot open makes the
  Bridge refuse to start with `lifecycle roots: lifecycle root "<path>" cannot be opened by the service
  user ...`, never "start without it" (D22).
- **Order and errors** (every refusal up to the count makes no native call): input shape
  `INVALID_INPUT`; `BRIDGE_READ_ONLY` `FORBIDDEN`; a recorded outcome for the same key is returned
  before any cap, the same key with another input `IDEMPOTENCY_CONFLICT`; root not configured or
  deeper than `max_depth` or not authorized `FORBIDDEN`, a Git top level missing or above the root
  `FORBIDDEN` (`non_git`), a `gitdir:`/`commondir` leaving the root `FORBIDDEN` (`not_in_root`), a
  missing path `NOT_FOUND`; `create_per_hour` reached `SESSION_LIMIT` (new code, retryable);
  then one bounded native read per configured root (5 s, fewer than 1000 entries each: a full window is "cap state unknown"; each root is checked against its startup identity first): `max_sessions`
  of the root or `max_total_sessions` reached `SESSION_LIMIT`; a count that is truncated, failed
  or unreadable is `SESSION_LIMIT` ("cap state unknown"), a count that timed out
  `UPSTREAM_TIMEOUT`, and neither writes anything.
- **The one native write** follows an identity re-check and is never retried or replayed: a
  timeout, a lost response, an unreadable answer or an answer without a valid session ID is
  `UNKNOWN_OUTCOME` (`retryable: false`; the message gives the `hb_key` hint to find a stray
  session through `list_catalog`), and the SAME key and input return that recorded outcome again
  without a second POST. A native 4xx/5xx is `UPSTREAM_FAILURE` and is not recorded (the
  caller's retry is a new request). A returned `directory` that differs from the requested path,
  or an identity that changed during the POST, is `UNKNOWN_OUTCOME` carrying
  `bootstrap_may_be_outside_root=true`; the session is not used.
- The idempotency registry and the hourly counter are memory-only (a restart resets them,
  ADR-022 3.3), the registry holds at most 10,000 records, and creates are serialized so the caps
  hold under concurrency.
- **Audit (ADR-022 S5).** Every call writes exactly one AUDIT-01 line `event=session_create`
  (message `session create`) on every return path (input refusal, `BRIDGE_READ_ONLY`, a replay, a
  conflict, every path refusal, the caps, a count timeout, `UPSTREAM_FAILURE`, `UNKNOWN_OUTCOME`,
  success, a panic, and a call the SDK rejects against the input schema, which the AUDIT-01b
  middleware writes). It is written by the deferred step of `call`, before the result is
  returned. Keys: `tool=create_session`; `outcome` (`created`, `replay`, `rejected`); `accepted`
  (`true` exactly when not `rejected`); `error_code` (only when rejected; `INTERNAL` for a panic);
  `root_id` (the index of the configured root, 0-based, only once the root is resolved; never the
  path); `session_id` (`opencode:ses_...`, only when the native answer carried a well-formed ID:
  on success and on the directory-mismatch `UNKNOWN_OUTCOME`); `key_hash` (the first 16 hex
  characters of sha256 of the idempotency key, the same value as the `hb_key` hint; absent for a
  key outside 8 to 128 bytes, whose hash would reveal it); `limit_state`
  (`hour:N/M,root:N/M,total:N/M`, the counts at the end of the call, with the new session and this
  POST included on success, `?` for a count that was not established **and for the root and total
  counts after any `UNKNOWN_OUTCOME` (timeout, no valid ID, directory mismatch, a failure after the POST):
  a session may exist that the count did not include**, absent when the call stopped before the
  hourly counter); `bootstrap_may_be_outside_root=true` (only on that
  `UNKNOWN_OUTCOME`); `replay=true` (only when a recorded error is returned again, never for a waiter whose own
  wait timed out; a recorded success has `outcome=replay`); `principal`. Never logged: title, subdirectory text, prompt,
  the raw key, the root path or any native response text. The dashboard ring keeps the event and
  these keys. The archive and close lines are in the S6 entry below.
- The tool is registered only when the server has a create configuration, which `cmd/bridge` builds when
  `BRIDGE_LIFECYCLE_CONFIG` is set and valid and the ingress is **not** read-only (D22,
  `cmd/bridge/lifecycle.go`). With the variable unset a deployed Bridge gains no lifecycle tool and keeps its
  base tools (eleven with `BRIDGE_PERMISSION_RELAY=off`, twelve with the default `reject_only` relay,
  `cmd/bridge/relay.go:12-32`); see "Registration and production wiring" for the full count.

### ADR-022 S6 and ADR-025: `archive_session`, `unarchive_session`, `close_session` (against a fake native; registered only when configured, wired by `cmd/bridge` from `BRIDGE_OVERLAY_DIR`, D22)

`archive_session {session_id}`, `unarchive_session {session_id}` and `close_session {session_id}` ->
`{session_id, archived, changed, archived_at?, interrupt?}`. `session_id` is `opencode:<id>` (the input
schema has that one property, `SessionInput`). `archived` is the overlay state after the call;
`changed` is false for an idempotent no-op (archiving an archived session, unarchiving one that is not
archived); `archived_at` (RFC 3339) is set by `archive_session` and `close_session`; `interrupt`
(`interrupted` or `none`) is set by `close_session` only (a `failed` interrupt never reaches a result: the
call is refused and the overlay is unchanged).

- **Registration.** The three tools exist only when the server has an archive configuration
  (`Server.Archive`, which needs a validated lifecycle configuration and an overlay store that holds
  the write role). Without it none is registered (the handler itself refuses `INVALID_INPUT` "unknown tool"
  when the configuration is absent). `cmd/bridge` builds it when `BRIDGE_LIFECYCLE_CONFIG` is set and valid
  **and** `BRIDGE_OVERLAY_DIR` names a valid overlay directory (ADR-025 Q5/Q6, D22): the directory is
  absolute, a real directory (no symlink in the final component), owned by the service user, mode `0700`; an
  invalid directory or an invalid overlay file is a startup error for the writer (D18 Q3). Not registered on a
  read-only ingress (the overlay is then opened as a reader for `list_catalog` and the dashboard only), and not
  registered by a second writer that did not get the overlay write lock (ADR-025 4.3); that second writer still
  refuses `send_prompt` to an archived session, from a snapshot a poller refreshes every 250 ms on top of the
  overlay's own once-a-second stat poll (about 1.25 s after the lock holder's archive; an unreadable file keeps the
  last good snapshot, ADR-025 4.4). `BRIDGE_OVERLAY_DIR`
  set without `BRIDGE_LIFECYCLE_CONFIG` is a startup error.
- **The overlay is a label store, never an authority** (ADR-025 5). It lives in the Bridge's own
  `overlay.json` (strict JSON, checksum, at most 1000 entries and 512 KiB, D18 Q10): an archived
  session is hidden from `list_catalog` unless `include_archived` is true (D16 Q5) and from the dashboard
  (`archived=1` shows it), and `send_prompt` to it is refused `FORBIDDEN` with reason `session_archived`
  from an in-memory snapshot with no I/O and no native call (D18 Q1). `list_sessions`, `find_sessions`,
  `get_session` and `get_repository` are unchanged (D18 Q2); no authorization decision reads the overlay.
- **No native write for archive or unarchive.** The preconditions are native READS. `close_session`
  makes at most one native write, the `POST /session/:id/abort` of the existing interrupt path, and only
  while a Bridge execution holds the session's guard.
- **Order and errors** (for `archive_session` and `unarchive_session`, every refusal up to the reservation makes
  no native write; for `close_session`, only the refusals **before its interrupt step** make none, because
  the interrupt, which can send the native abort POST, runs before the reservation: a refusal at the interrupt
  step or after it may already follow that abort):
  `BRIDGE_READ_ONLY` `FORBIDDEN`; not an `opencode:ses_...` session ID `INVALID_INPUT` (a `claudecode:` ID too:
  "claudecode sessions are not enabled on this Bridge"); outside the static ACL the
  existing refusals; lifecycle authorization with the `archive` mode: a missing directory `NOT_FOUND`, a
  bad input `INVALID_INPUT`, any other refusal `FORBIDDEN`, an unfinished directory check
  `UPSTREAM_TIMEOUT` (retryable); the overlay unreadable or this process without the write role
  `UPSTREAM_FAILURE` with reason `overlay_unavailable`. Then, for `close_session` only, the interrupt (below).
  Then the lifecycle reservation (a held execution guard or another lifecycle change `SESSION_BUSY`),
  then native reads, all fail-closed: the session not idle, a pending permission, an unfinished history
  message or an unreadable one, or a running child session `SESSION_BUSY`; an unreadable status, permission
  list or session tree (or a tree of 1000 sessions or more) `UPSTREAM_FAILURE`. Then the cap, a re-check of
  the authorization and of the native state, and the one overlay write.
- **Cap (D16 Q3).** `archive_per_hour` (default 10) is **one** hourly window shared by the three tools and
  by successful and failed overlay writes alike. A call is counted by `charge`, which runs after the first
  native preconditions and **before** the authorization/native re-check and the overlay write, so a failing
  write, and an idempotent no-op (archiving an archived session, unarchiving one that is not archived), spend
  budget too; a call refused earlier is not counted. The window is memory-only, a restart resets it. Reached: `SESSION_LIMIT` (retryable). A full overlay
  (1000 entries or 512 KiB) is `SESSION_LIMIT` too; entries are never dropped silently.
- **Overlay write errors.** A rename that fails changes nothing (`UPSTREAM_FAILURE`, reason
  `overlay_unavailable`); a write whose outcome is unknown (directory fsync, a hung rename) is
  `UNKNOWN_OUTCOME` with reason `overlay_uncertain` (D18 Q13): read `list_catalog` with
  `include_archived`, and repeat the call, which is idempotent; a call that cannot take the overlay's
  write lock within its bound because a previous write has not returned is `UPSTREAM_TIMEOUT`
  (retryable). The published snapshot never changes before the rename.
- **`close_session` is the interrupt, then the archive (D16 Q8).** If a Bridge execution holds the session's
  guard, the call validates the session, runs the manager's own `Interrupt` (no second audit line), and
  waits (5 s) until the execution is `interrupted`, `completed` or `failed` **and** the guard is released;
  `interrupt` is then `interrupted`. With no execution holding the guard it behaves as `archive_session`
  (`interrupt` is `none`). A failed or uncertain interrupt leaves the session un-archived: an execution that
  settled in the meantime answers `INVALID_INPUT` ("execution already terminal"), a submission still in
  progress `SESSION_BUSY`, one that does not settle within the bound `UNKNOWN_OUTCOME`. **The interrupt is
  not rolled back:** the call may interrupt a running execution and then still refuse the archive (a
  prompt raced in or another reservation `SESSION_BUSY`, a failed precondition, the cap `SESSION_LIMIT`, an
  overlay write failure or uncertainty); the execution stays interrupted and the caller reads the state
  and retries. `close_session` never deletes or abandons anything natively.
- **Audit (ADR-025 4.6).** Every call writes exactly one AUDIT-01 line (message `session lifecycle`) on
  every return path: `event` is `session_archive`, `session_unarchive` or `session_close`; `tool`;
  `outcome` (`archived`, `already_archived`, `unarchived`, `not_archived`, `closed`, `rejected`);
  `accepted`; `error_code` and `reason` (only when rejected); `session_id` (only when well formed);
  `root_id` (the index of the configured root, never a path); the closed counters `overlay_generation` and
  `overlay_entries` after the call; `archived_in_window` (when the call was counted); `interrupt`
  (`close_session` only); `principal`. Never logged: a path, a title, free text or the file contents. The
  dashboard ring does not keep these events until its allowlist is amended.
- **Not verified.** Everything above is tested against a fake native and the real overlay store; no live run
  was made (the live behaviour of an archived session on the native side is UNVERIFIED).
- **Sources (base `ebb2ce3`).** `internal/mcp/server.go:41-43,148-160,330-343` (registration, audit
  hook), `internal/mcp/archive.go:20-31,83-93,125-137,162-180,199-274,279-325,331-369` (output, errors, cap, order,
  preconditions, interrupt), `internal/mcp/audit.go:265-343`, `internal/overlay/overlay.go:41-43,609-720`
  (bounds, change and commit), `internal/execution/manager.go:282` (`session_archived`),
  `internal/execution/lifecycle.go:30-44` (reservation), `internal/mcp/catalog.go:225-238`
  (`include_archived`).

### Registration and production wiring of the five lifecycle tools

| Tool | Registered when (`internal/mcp/server.go`) | Built by `cmd/bridge` (D22, `cmd/bridge/lifecycle.go`) |
|---|---|---|
| `list_catalog` | `Server.Catalog != nil && Server.Catalog.Roots != nil` (`:330`) | `BRIDGE_LIFECYCLE_CONFIG` set and valid; also on a read-only ingress |
| `create_session` | `Server.Create != nil` (`:335`) | the same, and the ingress is not read-only |
| `archive_session`, `unarchive_session`, `close_session` | `Server.Archive != nil` (`:340`) | the same, plus a valid `BRIDGE_OVERLAY_DIR`, not read-only, and this process holds the overlay write lock |

The lifecycle surface is enabled by default (D22): no extra switch. `lifecycle.FromEnv` and
`BRIDGE_OVERLAY_DIR` are read at startup; unset, lifecycle is inert; an invalid value refuses to start.
The startup log names the registered lifecycle tools and the root count (no path). **Tool counts** (the base
is eleven; `reply_permission` is the twelfth under the default `reject_only` relay, `cmd/bridge/relay.go:12-32`):

| Configuration | `BRIDGE_PERMISSION_RELAY` default | `=off` |
|---|---|---|
| no `BRIDGE_LIFECYCLE_CONFIG` | 12 | 11 |
| config, no overlay | 14 (`list_catalog`, `create_session`) | 13 |
| config and overlay (writer with the lock) | 17 | 16 |
| config, read-only ingress (with or without overlay) | 13 (`list_catalog` only) | 12 |
Operator view: `docs/deploy/SESSION_LIFECYCLE_RUNBOOK.md`.

### ADR-024 S3: `reply_permission` (reject only, against a fake native; registered only when configured)

`reply_permission {execution_id, permission_id, decision, supersedes?}` ->
`{execution_id, permission_id, decision, outcome: "replied", attempt_id, attempt, decided_by:
"chat_client", human_verified: false}`. The input schema has no other property
(`additionalProperties: false`): `permission_id` matches `^per[A-Za-z0-9_-]{1,125}$`, `decision` is
`reject` or `allow_once` (never `always`), `supersedes` matches `^[A-Za-z0-9_-]{22}$` and is the only
optional field. A `message`, `reply`, `session_id` or any other field is rejected before the handler
runs. The tool carries the annotations `readOnlyHint: false`, `destructiveHint: true` (untrusted
hints, never authority). The decision is made by the Chat model, or by a human only if claude.ai's
per-call approval is configured: the server cannot verify which, and says so in every result.

- **The error object** gains three additive optional fields used only by this tool: `attempt_id` and
  `attempt` (only on `UNKNOWN_OUTCOME`, so the caller always has the value to quote in `supersedes`)
  and `reason`, a closed enum: `not_verified`, `unrecognized_kind`, `allow_once_disabled`,
  `policy_tool`, `policy_pattern`, `policy_scope`, `policy_rate`, `attempt_in_flight`,
  `already_replied`, `nothing_to_supersede`, `supersedes_mismatch`, `supersede_limit`,
  `supersede_allow_once_not_offered`, `resolved_elsewhere_or_dropped`, `audit_unavailable`,
  `native_rejected_id`. `reason` never replaces `code`; no error carries native text.
- **Order of checks** (ADR-024 4.2; every refusal before the attempt record makes no native write):
  `BRIDGE_READ_ONLY` `FORBIDDEN` (the tool is in `mutating()`; nothing is read natively);
  `allow_once` `FORBIDDEN`/`allow_once_disabled` (reject_only: configuration and input only, zero
  native calls, before the execution is even looked up); a Claude Code execution `INVALID_INPUT`;
  the execution exists and its session is authorized (the ACL read of `GET /session/:id` is the only
  native call so far); the execution is `waiting_for_permission` with the guard held by this
  execution, else `NOT_FOUND`/`resolved_elsewhere_or_dropped`; the in-memory record rules below (no
  permission read); then, under the run's own token (so it cannot interleave with a prompt, an
  interrupt, a reconcile probe or an ADR-020 release), a **fresh** permission-observer re-read of
  the session with its own directory routing: request absent or resolved `NOT_FOUND`/
  `resolved_elsewhere_or_dropped`; a request of another session, `unattributed`, `conflicting`,
  tool-less, verified for another execution, duplicated or outside the closed charsets `FORBIDDEN`/
  `not_verified`; observer unavailable `UPSTREAM_FAILURE`, timed out `UPSTREAM_TIMEOUT`; the
  workspace identity re-check; then the **attempt record** and only if it was written, the one native
  write `POST /permission/:requestID/reply` with `{reply: "reject"}` and no message.
- **Attempt record failure:** the writer (`internal/audit`, the one process-wide latch) reports
  failure, there are zero native writes and the call returns `UPSTREAM_FAILURE`/`audit_unavailable`
  (`retryable: true`); the record is `refused`. A failed superseding attempt record restores the
  earlier `unknown` attempt (it never reopens a fresh first attempt).
- **Record** (memory-only, keyed by `(execution_id, permission_id)`; empty after a restart): `replied`
  same decision returns the recorded result and sends nothing; `replied` other decision
  `IDEMPOTENCY_CONFLICT`; `unknown` or `in_flight` returns the recorded `UNKNOWN_OUTCOME` with its
  `attempt_id` and `attempt` and sends nothing; `refused` is re-evaluated. A native 404 is
  `NOT_FOUND`/`resolved_elsewhere_or_dropped` (record `refused`), a native 400 `UPSTREAM_FAILURE`/
  `native_rejected_id` (record `refused`), anything else (timeout, lost response, 5xx) is
  `UNKNOWN_OUTCOME` (`retryable: false`) and is never resent.
- **`supersedes`** is honored only from state `unknown`: `in_flight` `SESSION_BUSY`/`attempt_in_flight`
  (retryable); `replied` `IDEMPOTENCY_CONFLICT`/`already_replied`; `refused` or no record
  `INVALID_INPUT`/`nothing_to_supersede`; a value that is not the record's `attempt_id`
  `INVALID_INPUT`/`supersedes_mismatch`; a second supersede `FORBIDDEN`/`supersede_limit`; an
  `allow_once` record `FORBIDDEN`/`supersede_allow_once_not_offered`; another decision
  `INVALID_INPUT`. None makes a permission read or a native write. An accepted supersede re-reads,
  requires the request still pending, and is attempt 2 under a new `attempt_id` with
  `supersedes=<attempt_id>` in its attempt line (D15 Q8: one explicit superseding `reject`).
- **Attempt line** (`event=permission_reply phase=attempt`, AUDIT-01 shape, allowlisted closed-shape
  fields only; no new record type): `tool, attempt, attempt_id, supersedes?, decision, process_start,
  permission_id, execution_id, session_id, native_tool, kind, scope_digest, pattern_count,
  policy_mode=reject_only, policy_rule=reject_allowed, principal, decided_by=chat_client,
  human_verified=false`. The `phase=result` and `phase=refused` lines are slice S5; the generic
  AUDIT-01 writer writes no line for this tool.
- **Registration (superseded by S5, below):** in S3 the tool was registered only when the server had
  a `PermissionRelay` configuration and nothing in `cmd/bridge` set it. S5 sets it by default.
- **ADR-024 S1 changes in this slice:** `get_session` reports a measured `pending_permission_count`
  of `0` while the state is `waiting_for_permission` (it stays absent when the permission read failed
  or the state is not waiting); a `resolved_externally` tombstone of an execution no longer hides its
  one pending request from `pending_permission` (uncertain entries still do); the permission-source
  call has a deadline of its own.

### ADR-024 S5: `reply_permission` audit lines, production replier, default registration

- **Registration (D15 Q3).** `cmd/bridge` registers `reply_permission` in `reject_only` by default: a
  deployed Bridge now lists **twelve** tools (eleven plus this one; thirteen with `create_session`).
  `BRIDGE_PERMISSION_RELAY` is the switch: unset or `reject_only` registers it, `off` keeps the
  eleven-tool surface, `policy` and any other value refuse startup (policy mode, ADR-024 S2/S4, is
  not started: D15 Q1). The name and its values are a maintainer proposal (ADR-024 section 5 names
  the variable and the three values).
- **Production replier.** `opencode.Client.ReplyPermission`: `POST /permission/:requestID/reply` with
  the body `{"reply":"reject"}` and nothing else, in the session's directory (`?directory=`). It
  accepts only `reject` (never once or always) and only well-formed ids and an absolute directory;
  a refused call makes no request. The result of the HTTP call is classified by the manager: 404
  `NOT_FOUND`/`resolved_elsewhere_or_dropped`, 400 `UPSTREAM_FAILURE`/`native_rejected_id`, every
  other status (401, 403, 429 and 5xx included), a timeout or a lost response `UNKNOWN_OUTCOME`.
- **No production permission source yet (superseded by S6, below).** The fresh re-read of ADR-024 4.2 step 5 needs a
  `PermissionSource`, which needs a `PermissionObservationAuthority` built from the manager's
  submitted-run view; that is a slice of its own. Until it exists a deployed Bridge registers the
  tool but every call fails closed with `UPSTREAM_FAILURE` before any attempt line and any native
  write, and `pending_permission` stays count-only.
- **Audit lines (ADR-024 section 6).** One closed-shape `event=permission_reply` line per phase on
  every return path, written by `internal/audit` through the process-wide failure-reporting writer
  (same journal, lock and tee as every AUDIT-01 line; no new record type; no other tool's line
  changed): `phase=attempt` before the native write (its failure blocks the write); `phase=result`
  after it for a definite outcome or `UNKNOWN_OUTCOME` (`outcome` is `replied`, `not_found`,
  `native_rejected_id` or `unknown_outcome`, with `accepted` and, when not accepted, `error_code`
  and `reason`); `phase=refused` for every other return that sent nothing (read-only denial,
  `allow_once_disabled`, an unknown or Claude execution, a refused authorization, every closed
  refusal reason, an input the SDK rejected, the replay of a recorded `UNKNOWN_OUTCOME`) with
  `outcome=refused`, `accepted=false`, `error_code`, `reason`. A replay of a recorded success writes
  a `phase=result` line with `outcome=replay`. Every line carries `decided_by=chat_client` and
  `human_verified=false`. The values of a refused line come from the call's input reduced to their
  closed shapes (a malformed id, a free-text reason or an unknown error code never reaches the
  journal); a refused line carries no native request description and no attempt of its own.
  `policy_mode=reject_only`; `policy_rule=reject_allowed` on an attempt, `allow_once_disabled` on
  that refusal. The generic AUDIT-01 writer still writes nothing for this tool. The `kind` field has
  the shape the Bridge accepts from the observer, `[A-Za-z0-9_.:-]{1,64}` (the same closed charset as
  `native_tool`); a kind outside it is refused as `not_verified` before any attempt line. The result
  and refused/replay lines are written on a context that outlives the caller's, with a 5 s bound of
  their own (pinned by tests that cancel the caller after the native write).
- **Latch and a failed result line (step 9).** After any failed audit write every later
  `reply_permission` call is refused with `UPSTREAM_FAILURE`/`audit_unavailable` (`retryable: true`)
  before any native call, replays that would send nothing included (S5 design of review #197; the
  latch is process-wide and recovers only with a restart). A `phase=result` line that cannot be
  written after the native write leaves the real outcome and the record as they are, is logged
  loudly and latches the writer.
- **Bound.** The attempt write honours the call's context (review #277 P3-2): a journal that does not
  return before the 5 s bound latches the writer and the call refuses; a canceled caller refuses the
  call without latching. The line may still reach the journal later (an attempt line without a
  native write, never the reverse). Result and refused lines are written on a context that outlives
  the caller's, with their own 5 s bound.
- **Hardening of the S3 reviews.** A replier that panics settles the record to `unknown` and the call
  returns `UNKNOWN_OUTCOME` with its `attempt_id` (an explicit supersede then works); the id bounds
  agree (`permission_id` up to 128 characters, session id up to 256, in the tool, the manager and
  the audit field); waiting for a slot or the run token past the deadline is a retryable
  `UPSTREAM_TIMEOUT`; the manager refuses `allow_once` before any native read; the workspace identity
  re-check before the write refuses a replaced workspace (`FORBIDDEN`) and a filesystem timeout
  (`UPSTREAM_TIMEOUT`, retryable) with no native write and no attempt line.

### ADR-024 S6: production `PermissionSource`, `pending_permission`, runbook

`cmd/bridge` now installs the production `PermissionSource` together with the replier and the attempt
writer when `BRIDGE_PERMISSION_RELAY` is unset or `reject_only` (mode `off` installs nothing).
`allow_once` stays unregistered and refused (`FORBIDDEN`/`allow_once_disabled`, zero native calls):
the decision is D12 exception (b), the policy mode is D15 Q1 (off).

- **The source** (`internal/execution/permissionsource.go`): for every call a **fresh**
  `PermissionObserver` of the session and one bounded `Snapshot` (the observer's own 5 s bound for the
  whole call, inside the manager's read deadline); nothing is cached, so the view behind a reply and
  the view behind `pending_permission` are each a new native read through the session's own directory.
  Attribution is only the observer's. The observer's trusted authority is taken from the manager's own
  runs: the scope (session, the directory the run was accepted in, endpoint, native version `1.19.4`,
  principal `bridge`, policy revision `1`, opaque directory references) from the run that holds the
  session guard (a session nobody runs has no scope), and the executions (execution ID, the
  Bridge-chosen user message ID, submission attempted) from the manager's submitted runs of that
  session. No value comes from a native payload or from model text.
- **Native incarnation evidence (ADR-024 A1 S7, D21): off by default.** The observer verifies a request
  only when the scope carries an epoch, and `docs/P2_PERM_CORE_02_OBSERVATION.md` forbids fabricating
  one. `BRIDGE_PERMISSION_INCARNATION` (`off`, the default and when unset; `observed`; anything else is a
  startup error) decides. With `off`, or with `BRIDGE_PERMISSION_RELAY=off`, there is no source: on a
  deployed Bridge every request stays `unattributed`, `pending_permission` is count-only
  (`pending_permission_count`) and `reply_permission` refuses with `FORBIDDEN`/`not_verified`, with no
  native write and no attempt line. With `observed` (and `reject_only`) the Bridge subscribes once to
  OpenCode's `GET /global/event` and keeps a **Bridge-observed continuity epoch**: a random 128-bit token
  it generates itself, replaced at every (re)connection, on any `server.instance.disposed`, on
  `global.disposed` (and on any event type containing "dispos"), on stream end or error, on an event it
  cannot decode and when no event has been seen for 25 s; it is **armed** only after the first
  `server.heartbeat` of a connection (the hello does not arm). A run records the epoch armed when it was
  admitted; the scope carries an epoch only while it is armed and equal to the run's, and a view that ends
  under another epoch attributes nothing (`unattributed`, reason `epoch_changed`). So a run admitted
  before a dispose, a reload, a reconnect or a gap, or while the epoch was not armed, can never be
  verified (the human answers natively). The epoch is **continuity of observation, not native identity**
  (D21 Q1) and only ADDS a condition: every check of ADR-024 4.2 and ADR-018 section 5 still has to hold,
  replies stay reject-only, `allow_once` stays refused with zero native calls. Windows that remain:
  event latency (milliseconds) and a silent drop (up to about 25 s) between a native replacement and the
  Bridge noticing it. The seam is unchanged: `execution.NativeIncarnation`
  (`PermissionSourceConfig.Incarnation` and `Manager.SetIncarnation`, installed together by
  `cmd/bridge`); an unarmed source returns `("", nil)`. Not run against a live OpenCode (UNVERIFIED).
- **`pending_permission`** (on `get_execution_status` and `get_execution_result`, only while the state
  is `waiting_for_permission`, only for the one request verified for **this** execution):
  `{permission_id, call_id, message_id, tool, kind, pattern_count, scope_digest, attribution:
  "verified"}`. No pattern, no metadata and no native text; every field has a closed charset and an
  entry outside it is not exposed. `pending_permission_count` (an integer, on `get_session` and the
  two execution tools) counts this session's pending requests, attributed or not.
- **Error codes of `reply_permission`** (the closed set; `reason` is the enum of S3): `INVALID_INPUT`
  (input, `nothing_to_supersede`, `supersedes_mismatch`), `FORBIDDEN` (`BRIDGE_READ_ONLY`,
  `allow_once_disabled`, `not_verified`, `supersede_limit`, `supersede_allow_once_not_offered`,
  `unrecognized_kind`), `NOT_FOUND` (`resolved_elsewhere_or_dropped`), `SESSION_BUSY`
  (`attempt_in_flight`), `IDEMPOTENCY_CONFLICT` (`already_replied`, another decision),
  `UPSTREAM_FAILURE` (observer unavailable, `native_rejected_id`, `audit_unavailable`),
  `UPSTREAM_TIMEOUT` (the read or a wait exceeded its bound; retryable), `UNKNOWN_OUTCOME` (the native
  write may or may not have happened; never resent; carries `attempt_id` and `attempt`).
  A view made uncertain by a conflicting entry (for example an assistant message of another
  workspace) is `UPSTREAM_FAILURE` without a reason: the whole view fails closed.
- **Audit fields.** Every `event=permission_reply` line (`phase=attempt`, `result`, `refused`) carries
  `decided_by=chat_client` and `human_verified=false`; the same two values are in the tool result.
  They say that the decision was made through the Chat client and that the server cannot tell whether
  a human approved the call; they are never an authorization input. Operator procedure and the live
  check: `docs/deploy/PERMISSION_RELAY_RUNBOOK.md`.

### ADR-023 dashboard (read-only; disabled by default)

The Bridge can serve a local, read-only dashboard (ADR-023). It is **off** unless exactly one of
`BRIDGE_DASHBOARD_SOCKET` or `BRIDGE_DASHBOARD_ADDR` is set (setting both is a startup error). It has no
write route, no SSE and no mutating call; every route answers only `GET` and `HEAD`. Operator setup and the
SSH forwarding recipe: `docs/deploy/DASHBOARD.md`.

| Variable | Meaning |
|---|---|
| `BRIDGE_DASHBOARD_SOCKET` | Path of a unix socket in a `0700` directory owned by the service user (the recommended transport, D17 Q1). |
| `BRIDGE_DASHBOARD_ADDR` | `host:port` where host is exactly `127.0.0.1` or `::1`; any other address is a startup error. Requests need a `Host` of `localhost`, `127.0.0.1` or `[::1]`. |
| `BRIDGE_DASHBOARD_REDACT_EXTRA` | **Slice-5a proposal name** (the ADR names none): a JSON array of at most 16 RE2 patterns of 1 to 256 bytes, additive to the shipped redaction list (always on). An invalid value is a startup error and is never logged. |

- **Routes:** `/` (session list), `/session/<harness>/<id>` (detail, executions, diffs),
  `/session/<harness>/<id>/turn/<message>` (one turn's diff), `/api/state` and
  `/api/state/session/<harness>/<id>` (the JSON view models the page polls: 5 s list, 2 s detail),
  `/audit` and `/api/audit` (the 1000-entry audit ring), `/dashboard.css`, `/dashboard.js`.
- **Security checks on every route:** a `Host` not on the allowlist (TCP) is 400; an `Origin` header
  whose host is not on the allowlist is 403, and on the unix socket any `Origin` is 403; a method
  other than `GET`/`HEAD` is 405; four data requests and eight requests in flight at once, the rest
  refused at once (503, never queued); a 5 s bound per request; responses carry a CSP and
  `Cache-Control: no-store`.
- **Rows (D17 Q6):** exactly the sessions the existing authorization lists (the ACL, the same set as
  `list_sessions`, sorted by native ID), then one pending row per `claudecode:` ACL entry (no native
  call, "ADR-021 pending"). When the lifecycle catalog is configured (ADR-022), each row carries
  `lifecycle_root`, the lifecycle root that contains the session directory, **as a label only**: the
  rule is that of `list_catalog` (ADR-022 6.1), so a session under a lifecycle root that the ACL does
  not list is **not** shown (reading it would need the O2 flag, D16 Q7). The production Bridge wires the
  lifecycle configuration from `BRIDGE_LIFECYCLE_CONFIG` (D22), so the label appears when it is set.
- **Archived (D17 Q7):** sessions the archive overlay reports archived are hidden by default; the query
  `archived=1` (exactly that value, on any data route) shows them with an `archived` marker. The
  overlay is the persisted overlay of ADR-025 when `BRIDGE_OVERLAY_DIR` is set (D22), otherwise empty. An
  overlay that cannot answer fails the call (503), as `list_catalog` does: a session is never listed or
  hidden by a guess.
- **Redaction:** diff paths and patches and the audit ring values are redacted with the shipped list
  (`sk-ant-`, `ghp_`, `github_pat_`, `AKIA...`, private-key blocks, bearer tokens) plus the operator
  patterns. A convenience, not a guarantee.
- **JSON model fields** (`/api/state*`): `harness`, `id`, `title`, `root` (the session directory),
  `lifecycle_root` (omitted when none), `archived` (omitted unless true), `state`, `guard`,
  `current_execution`, `last_activity`, `href`.
- **Status:** the native response shapes behind the diffs are read from upstream source and are
  **UNVERIFIED against a live server**; the live acceptance (R-DASH) is **NOT_EXERCISED**.

### Discovery bootstraps native instances (H4A-F2, 2026-11-05)

Read-only discovery and inspection tools (`list_sessions`, `find_sessions`,
`list_repositories`, `get_repository`, `get_session`, `get_harness_capabilities`)
can make OpenCode load (bootstrap) the instance of any ACL'd session directory,
also on a `BRIDGE_READ_ONLY=1` Bridge. Every ACL lookup (`GET /session/:id`) is
routed to the session's own directory, also for a workspace-bound session: OpenCode
keeps every `GET /session…` request on the local control plane and forwards only
other session calls (prompt, abort) and `/session/status`/`/permission` with
`?workspace=` to a workspace target (corrected 2026-11-05, see
docs/design/F2_WORKSPACE_ROUTING.md). The directory-scoped status, permission,
`/agent` and `/doc` reads target that directory too. OpenCode v1.19.4
(`anomalyco/opencode` `148842ef`) boots each directory once and caches it
(`project/instance-store.ts:108-124`). Bootstrap merges every `opencode.json(c)`
and `.opencode` directory from that directory up to the worktree root (`/` for a
non-git directory), writes `.gitignore`, starts a background npm install with
scripts disabled, and **imports and executes the plugins** found there
(`project/bootstrap.ts:30-43`, `config/config.ts:420-478`,
`config/plugin.ts:18-29`, `plugin/loader.ts:135-140`, `plugin/index.ts:114-124`,
`:181-253`). Details are in docs/ROADMAP.md, "F-2 verification note".

Therefore `BRIDGE_ALLOWED_SESSIONS` and `BRIDGE_ALLOWED_ROOTS` must contain only
sessions in workspaces whose `.opencode` content, along the whole path up to the
worktree root, the operator trusts. Listing sessions is not side-effect free in
OpenCode even though the Bridge itself executes nothing. A workspace-bound session
(non-empty `native_workspace_id`) is served by its workspace's instance, so the
Bridge makes no directory-scoped discovery request for it. `send_prompt` preflight
is unchanged and still reads directory-scoped status and permission for the
prompted session.

Session fields gain optional repository/workspace objects, parent_session_id,
harness_instance_id, native_project_id, native_workspace_id, native_state and
native_state_inferred_from_absence, and (F-5, 2026-11-06) native_unfinished_message.
Existing required fields remain. Omitted
native_state means native status was unavailable (failed read); see "Session state
and the Bridge guard" for when absence from a successful read is reported as idle.

### Session state and the Bridge guard

Accepted by the maintainer 2026-11-04 (ADR-012 state semantics, option b). Applies to
`get_session`, `list_sessions` and `find_sessions`. Precedence: a failed permission
read gives `unknown`; otherwise a pending native permission for the session gives
`waiting_for_permission`; otherwise:

- **This Bridge holds the session's busy guard:** state follows the held execution and
  is never `idle`. `submitting` (including a reservation not yet submitted) and
  `running` give `running`; `waiting_for_permission` stays `waiting_for_permission`
  (a pending permission cannot release the guard); `unknown` gives `unknown`. Native
  status is ignored for `state` and is reported only in `native_state`.
- **No guard:** native status maps explicit `idle` to `idle`, `busy`/`retry` to
  `running`, and unrecognized (for example a future native type) or unavailable
  (failed status read) status to `unknown`.
- **Absent from native status (F-4, 2026-11-04):** OpenCode 1.19.4 deletes a session
  from its per-directory status map when it becomes idle and reports `idle` for any
  absent session, so `GET /session/status` lists only non-idle sessions. A session
  absent from a *successful* status read of its own directory-scoped client is
  reported with `native_state: "idle"` and `native_state_inferred_from_absence: true`.
  `state` is `idle` only if, in addition, the permission read succeeded, no native
  permission is pending for the session and this Bridge holds no guard for it; the
  precedence above still applies (failed permission read -> `unknown`, pending ->
  `waiting_for_permission`, held guard -> never `idle`). An explicit native `idle`
  entry omits `native_state_inferred_from_absence`. A failed status read omits both
  fields and never yields `idle`; a 2xx `null` status body counts as a failed read.
- **Workspace-bound sessions are excluded from absence inference (R2-PR16-REVIEW,
  2026-11-04):** OpenCode 1.19.4 routes session-bound requests by the session's
  `workspaceID`, but `GET /session/status` carries no session ID and is served by the
  `?directory=` instance, so a session with a non-empty `workspaceID` can be busy in
  its workspace instance and still be absent from the map the Bridge reads. For such a
  session absence gives `state: "unknown"` with `native_state` and
  `native_state_inferred_from_absence` omitted. Since H4A-F2-FOLLOWUP (2026-11-05)
  discovery makes no directory-scoped status or permission request for such a
  session at all, so it is always reported `unknown` without `native_state`
  (a held Bridge guard maps as for a failed permission read: `unknown`). Its
  repository/workspace objects still come from local filesystem inspection.
- **Unfinished native assistant message (F-5, 2026-11-06):** an OpenCode restart
  during a tool call can leave an assistant message without `time.completed` while
  the session is absent from native status. `send_prompt` admission refuses such a
  session (`SESSION_BUSY`, "unfinished native assistant message"), so discovery must
  not call it idle. When the rules above would give `state: "idle"` (observed or
  inferred native idle, permission read succeeded, none pending), the Bridge reads the
  session's native history and applies the same predicate admission uses
  (`opencode.UnfinishedAssistant`: any assistant message with
  `time.completed <= 0`):
  - an unfinished assistant message gives `state: "unknown"` and
    `native_unfinished_message: true` (field omitted otherwise);
  - a history read that fails, times out, is refused by the bounded-call limit or
    returns a 2xx `null` body gives `state: "unknown"`, never `idle`;
  - all assistant messages completed (or none) leaves `idle` unchanged.
  `native_state` and `native_state_inferred_from_absence` are reported as before. A
  held Bridge guard still wins (state follows the guard). Pending-permission,
  busy/retry and workspace-bound sessions are not idle, so their history is not read
  for state and their state is unchanged. A session for which this Bridge holds the
  guard is not read either (the guard decides its state), so it never carries
  `native_unfinished_message`. History reads go through the F-3
  `internal/bounded` policy: one in-flight read per (endpoint, directory, session,
  page size), at most 8 running, each bounded to 3 s.
  - **`get_session` is authoritative:** it reads the full history, exactly as
    admission does.
  - **`list_sessions` / `find_sessions` read only the latest page:** they use
    OpenCode's bounded latest-page route (`GET /session/{id}/message?limit=10`; OpenCode
    1.19.4 `148842ef`, `packages/opencode/src/server/routes/instance/httpapi/handlers/session.ts:106-128`
    and `packages/opencode/src/session/message-v2.ts:438-479`, newest rows by
    `time_created DESC` with `LIMIT n+1`). At most four reads run at once, and the
    whole phase is bounded to 3 s. Sessions not checked within it are `unknown`;
    the listing itself does not fail. An unfinished assistant message older than
    the latest 10 messages is not seen there, so list/find may show `idle` where
    admission refuses. Call `get_session` before a mutation.

The guard is per Bridge process: another Bridge or native client is not visible
(ADR-010). The guard is read without native I/O. Session state is a non-atomic
observation and never completion proof; use execution status/result. In particular,
`idle` (explicit or inferred from absence) only means no native work was observed for
the session at read time: it does not prove that a submitted prompt ran, finished or
succeeded, and absence cannot distinguish "finished" from "never started", "aborted"
or a native instance that lost in-memory status (for example after restart).
Parent links require separate authorization; unapproved children stay invisible.
Repository/workspace hashes and endpoint IDs are metadata locators, not grants.
Canonical path authorization now also rejects symlink aliases and unclean paths.
See ADR-012/013 and H4_A_EVIDENCE for exact support and limitations.


H4-A review correction: list_sessions now shares the <=1000-ACL/10s registry
pipeline and explicit oversize/deadline errors, preserving its input and success
shape. Directory evidence is shared per request only (four workers/request, two
admitted discoveries/Server), never presented as atomic state. Optional display_name
is printable and bounded to 128 characters; workspace linked_worktree is optional.
No remote URL is exposed. Confined metadata reads replace Git subprocesses; unsafe
storage leaves repository/workspace absent. See H4_A_REVIEW_FIXES for exact Git
layout limits, installed history ordering, authorization separation and rollback.

H4-A F-3 (blocking storage): repository/workspace inspection runs off the request
path. If a storage call blocks (for example a hung NFS or FUSE mount), the caller
stops waiting at the request deadline or after 2 s, whichever is first, and the
session is returned with repository/workspace absent, exactly as for unavailable
storage; no partial or fabricated identity is reported. Concurrent requests for
the same session policy and directory share one in-flight inspection; at most 16
inspections run at once per Bridge process, and further ones are treated as
unavailable without touching storage. A result that completes after every waiting
request has returned is discarded, not cached; the next request inspects again.
A storage call that never returns cannot be cancelled from Go: its inspection keeps
its slot until the call returns, which may only happen when the Bridge restarts.
Until then every request for that policy, session and directory waits the request
deadline or 2 s (whichever is first) and gets repository/workspace absent, and
once 16 such inspections are stuck every repository/workspace lookup in that
Bridge process is unavailable (fail closed). Recovery from a permanently hung
mount may therefore require a Bridge restart.

H4-A F-3 (workspace authorization check): every session-scoped tool and discovery
check that the session's directory is canonical and under an approved root
(`EvalSymlinks`, root `Stat`). That check now runs through the same bounded
mechanism as repository inspection (`internal/bounded`): it stops waiting at the
request deadline or after 2 s, concurrent checks of one policy and directory
share one in-flight check, at most 16 checks run per Bridge process, and no
result is cached. A check that does not complete is never treated as allowed.
The tool returns `{code: "UPSTREAM_TIMEOUT", retryable: true}` ("workspace
authorization check did not complete; storage unavailable") instead of
`FORBIDDEN`, because the outcome is unknown rather than a denial; discovery
tools (`list_sessions`, `find_sessions`, repository tools) return the same error,
and a parent link whose check does not complete is not shown. For
`send_prompt` and `interrupt_execution` this happens before admission, so nothing
is submitted to the native harness and the call may be retried. A check stuck in
a call that never returns keeps its slot as described above.

**`interrupt_execution` result and aborted tool parts (H4-F1, H4-F2).**

- `interrupt_execution` returns the execution state **at request time**. It can
  still be `running`: the native abort is asynchronous and the Bridge does not
  wait for the turn to end. The client must poll `get_execution_status` for the
  terminal state (`interrupted`, or whatever the native evidence proves).
- Native part status is not outcome evidence. After an abort, an OpenCode `bash`
  tool part can read `status: completed` with no exit code. The Bridge's
  `interrupted` comes from the assistant message's `MessageAbortedError`
  (`info.error`), never from a part. Never read a part alone as success, and do
  not treat `completed` on a tool part of an aborted turn as the command having
  finished.

Execution manager workspace checks (EXEC-FS-BOUND-IMPL, 2026-11-05;
docs/design/EXEC_FS_BOUNDING.md). The `EvalSymlinks`/`Stat` identity checks inside
the execution manager run through their own bounded group: 2 s per call or the
caller's deadline, at most 8 running, one in-flight call per operation and path,
no caching. A check that does not complete is never a pass and never an identity
mismatch:

- **`send_prompt` admission:** the session reservation is released, nothing is
  submitted, the key is not accepted. `UPSTREAM_TIMEOUT`, `retryable:true`. A retry
  with the same key is a new admission.
- **`send_prompt` replay (same key and payload):** `UPSTREAM_TIMEOUT`,
  `retryable:true`, and no execution snapshot. Retrying later replays the
  original execution.
- **Accepted, before the native prompt call:** the run ends `failed` with
  `local_failure.code = PRE_SUBMISSION_UNAVAILABLE` and the guard is released
  (the prompt was provably not invoked). The key stays bound to that failed run;
  a replay returns it and never resubmits.
- **`interrupt_execution`:** no native abort is sent. `UPSTREAM_TIMEOUT`,
  `retryable:true`. State and guard are unchanged.
- **Execution status and result** (and the MCP binding check before them):
  `UPSTREAM_TIMEOUT`, `retryable:true`, nothing exposed.
- **Observation after submission:** the execution stays or becomes `unknown`
  with `unresolved.reason = OBSERVATION_UNAVAILABLE`. It is never promoted and
  the guard is kept. The check of a correlated assistant message's working
  directory runs outside the execution's lock, so status reads, the session
  guard and discovery never wait on it.
  The re-validation after the native reads and that working-directory check
  never join a check that started earlier (R5-PR67-REVIEW P3-1): they always
  start their own call, so they observe the filesystem after the reads. Checks
  before the reads may share an in-flight call of the same operation and path.

`retryable:true` is set only where nothing was or will be submitted to the native
harness.


## ADR-014 execution identity and bounded admission (isolated review proposal)

Original seven and candidate eleven tool names, required inputs and output shapes
remain unchanged. There is no new adapter/runtime dependency or configuration.
Workspace validation additionally compares the accepted directory's os.FileInfo
identity using os.SameFile, including same-key and cached status/result reads.
Observed replacement returns FORBIDDEN without result content; internal Snapshot
remains authorization evidence and terminal records remain immutable.

### Accepted but never submitted (P2-REC-02B)

A failed fresh pre-Prompt metadata/identity check with no Runner.Prompt invocation
records an immutable local state=failed. Optional Execution.local_failure contains
{code: "PRE_SUBMISSION_UNAVAILABLE", recorded_at: <Bridge UTC timestamp>}.
Execution.error gives a sanitized local explanation. This is not a new MCP domain
error code. The status/result tool succeeds with a failed execution, as for other
retrievable execution outcomes. Local failure has no unresolved, ended_at or
reconciled_at; result messages is an empty array. The allocated message_id remains
correlation metadata and is not evidence of a native user message.

The internal submissionAttempted marker, not any error/reason string, determines
whether local finalization is allowed. It is set conservatively immediately before
native Prompt invocation and never cleared. Once invocation is attempted, timeout,
disconnect and missing history cannot prove non-submission: keep unknown and the
busy guard until native proof. Never automatically retry Prompt.

Local finalization retains execution/message IDs, key and fingerprint, releases
only that execution's session guard, and rejects interrupt_execution as
INVALID_INPUT without native Abort. Same-key/payload replay returns the same local
terminal outcome subject to existing authorization/workspace checks; changed
payload conflicts. A different key requires full fresh admission. Delayed reads,
probes and Close cannot change the local outcome or release a newer run's guard.
No guard-reset tool or native session mutation is introduced.

local_failure is additive and optional; existing seven and eleven tool names,
required inputs and required output keys remain unchanged. Native execution
failure has local_failure absent and native ended_at when terminal proof exists.
Clients that assumed every failed execution had native assistant output/ended_at
must handle local failure explicitly. Strict output-field allowlists need the new
optional field; no blanket strict-parser compatibility claim is made.

New reserved manager preflights share four admission slots and a five-second
budget including slot queueing; these are separate from reconciliation slots.
Current MCP authorization precedes this budget; same-key followers wait on the
original admission or caller cancellation. Admission deadline/cancellation before
acceptance releases its reservation without native Prompt. Close cancels and joins
managed admission cleanup without issuing native Abort. Caller cancellation after
acceptance does not cancel native work. The 300-second background monitor and
existing operator-controlled restart reconciliation boundary remain unchanged.

This detects observed replacement; it is not an atomic native-filesystem lease,
full isolation or protection from inode reuse, mount changes or swaps between
checks. Existing sessions whose workspace spelling is noncanonical are rejected.
