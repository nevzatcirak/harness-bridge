# MCP contract v0.1 (proposed; current implementation has a limited subset)

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
idempotency, timeout and restart semantics and the outstanding live-test gate.
