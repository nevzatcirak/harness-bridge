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
    "error": {"type":"object","required":["code","message","retryable"],"properties":{"code":{"enum":["UNAUTHORIZED","FORBIDDEN","NOT_FOUND","SESSION_BUSY","INVALID_INPUT","UPSTREAM_TIMEOUT","UNKNOWN_OUTCOME","UPSTREAM_FAILURE"]},"message":{"type":"string"},"retryable":{"type":"boolean"},"execution_id":{"type":"string"}}}
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
