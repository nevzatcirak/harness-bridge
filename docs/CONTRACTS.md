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
`session_id`, `native_id`, `harness`, `directory`, `title`, and conservative
`state:unknown` (execution state is separate). Execution IDs and message IDs are
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
`state` (conservatively `unknown`). Execution fields: `execution_id`, `session_id`,
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
  workspace authorization before reading the native inventory/specification.
- `read_session_history` retains session_id and messages; optional limit (1–200)
  and before (opaque native cursor, <=2048 URL-safe characters) enable bounded
  native pages; optional next_cursor comes from X-Next-Cursor. Omit both inputs
  for old behavior. No promised numeric total, snapshot consistency or synthetic
  cursor; an upstream server ignoring the limit fails closed.

Session fields gain optional repository/workspace objects, parent_session_id,
harness_instance_id, native_project_id, native_workspace_id, native_state. Existing
required fields remain. Absent native state means unknown, never assumed idle.
Parent links require separate authorization; unapproved children stay invisible.
Repository/workspace hashes and endpoint IDs are metadata locators, not grants.
Canonical path authorization now also rejects symlink aliases and unclean paths.
See ADR-012/013 and H4_A_EVIDENCE for exact support and limitations.
