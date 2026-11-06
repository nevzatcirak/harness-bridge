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
    "error": {"type":"object","required":["code","message","retryable"],"properties":{"code":{"enum":["UNAUTHORIZED","FORBIDDEN","NOT_FOUND","SESSION_BUSY","IDEMPOTENCY_CONFLICT","INVALID_INPUT","UPSTREAM_TIMEOUT","UNKNOWN_OUTCOME","UPSTREAM_FAILURE"]},"message":{"type":"string"},"retryable":{"type":"boolean"},"execution_id":{"type":"string"}}}
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
Session IDs match `^opencode:ses[A-Za-z0-9_-]+$`, max 265 characters, with native
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


Optional `BRIDGE_READ_ONLY=1` preserves the seven tools but returns FORBIDDEN for
send_prompt and interrupt_execution before native mutation. Default/0 preserves
existing write behavior; invalid values fail startup. It is required for the
separate staged private listener while the local Bridge remains a possible
writer; independent processes do not share execution/idempotency state.

## H3 single-writer admission and replay

Same session/key with a different prompt returns `IDEMPOTENCY_CONFLICT`,
`retryable:false` (previously INVALID_INPUT). Identical concurrent requests wait
through admission and return the same execution/message IDs once accepted.
Different keys on an active/uncertain session return SESSION_BUSY. Preflight
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
| `unresolved.reason` | `MONITOR_EXPIRED`, `SUBMISSION_UNCONFIRMED`, `OBSERVATION_UNAVAILABLE`, `ABORT_UNCONFIRMED`, `IDENTITY_MISMATCH` or `MANAGER_CLOSED` |
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
| `liveness.stall_reason` | `COMPLETED_WITHOUT_TERMINAL_EVIDENCE` (correlated assistant completed, no `finish` stop/end_turn or named error, native status absent, no pending permission), `NO_CORRELATED_ASSISTANT` (user message present, no correlated assistant, status absent) or `NO_PROGRESS` (`busy`, or `retry` whose `next` is past, and no native progress, nor submission, for longer than 2 minutes) |
| `liveness.observed_at` | Bridge time of the probe that produced the object |

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
  for state and their state is unchanged. History reads go through the F-3
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
