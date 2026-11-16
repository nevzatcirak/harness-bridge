# Permission relay: operator runbook (ADR-024, `reject_only`)

`reply_permission` lets Claude Chat **reject** a pending native OpenCode permission request of an
execution the Bridge started. It can only reject: `allow_once` is not offered (decision D12, exception
(b)) and the policy mode is off (D15 Q1). It is **registered by default**; this page says how to turn
it off, what it does today, and how to check it live. Status: implemented and tested against fakes;
the live acceptance below is **not yet exercised**.

## What it does, in one paragraph

When an execution is `waiting_for_permission`, `get_execution_status` may show a `pending_permission`
object (tool, kind, pattern count, a digest; never a pattern or native text) for the one request the
Bridge can attribute to that execution. `reply_permission {execution_id, permission_id, decision:
"reject"}` re-reads the native request list fresh, checks that request belongs to that execution,
writes an audit line, and sends exactly one native `POST /permission/:id/reply` with `{"reply":"reject"}`.
Every refusal before the audit line sends nothing.

## Today's limit: nothing can be verified yet

The Bridge attributes a request to an execution only with independent native incarnation evidence, and
no such source exists (`docs/P2_PERM_CORE_02_OBSERVATION.md`: none is fabricated). So on a deployed
Bridge **every request stays `unattributed`**: `pending_permission` is absent, only
`pending_permission_count` is shown, and `reply_permission` answers `FORBIDDEN` with reason
`not_verified`, no native write, no attempt line. The startup log says so:
`permission relay: reject_only; no native incarnation source exists ...`. Answer such a request in
OpenCode itself. The decision on what can supply the evidence is open (BACKLOG, ADR-024 S6).

## Enable, disable

| `BRIDGE_PERMISSION_RELAY` | Result |
|---|---|
| unset or `reject_only` | the tool is registered (twelve tools; thirteen with `create_session`), the OpenCode client is the one replier, the audit writer is installed |
| `off` | nothing is installed; the Bridge keeps its eleven tools |
| `policy` or anything else | the Bridge refuses to start (policy mode is not started) |

`BRIDGE_READ_ONLY=1` refuses the tool (it is a native mutation, like `interrupt_execution`), nothing is
read natively. Recommendation (D15 Q7): configure claude.ai's **per-call approval** for
`reply_permission`; the tool carries `destructiveHint: true` so a client that honours it asks a human
first. The server cannot see whether it did: every result and audit line says `decided_by=chat_client`
and `human_verified=false`.

## What a call returns

| Situation | Result |
|---|---|
| `allow_once` | `FORBIDDEN` / `allow_once_disabled`; zero native calls |
| request not verified for this execution (today: always) | `FORBIDDEN` / `not_verified` |
| request already answered or gone | `NOT_FOUND` / `resolved_elsewhere_or_dropped` |
| native read failed or timed out (5 s) | `UPSTREAM_FAILURE` / `UPSTREAM_TIMEOUT` (retryable) |
| audit journal unavailable | `UPSTREAM_FAILURE` / `audit_unavailable` (retryable); see below |
| the write was sent, the answer lost or non-definite | `UNKNOWN_OUTCOME` with `attempt_id`; never resent |
| rejected | `{outcome: "replied", decided_by: "chat_client", human_verified: false}` |

After an `UNKNOWN_OUTCOME`, look at the request in OpenCode first. One explicit second `reject` is
allowed by passing `supersedes: <attempt_id>`; a third is refused. The record is memory-only: a Bridge
restart empties it.

## Audit lines

One `event=permission_reply` line per phase (`attempt` before the native write, `result` after it,
`refused` for every call that sent nothing) in the same journal as the other AUDIT-01 lines, each with
`decided_by=chat_client` and `human_verified=false`. If a line cannot be written the call refuses and
**every later `reply_permission` call is refused** (`audit_unavailable`) until the Bridge restarts.
That is deliberate: no permission mutation without a durable record.

## Live check (R5b style): operator present, disposable root

Do not read provider or harness configuration files for this. Use a disposable root and a session the
Bridge may use. Record the native request log, the Bridge journal and the tool results; anything not
run is UNVERIFIED, not PASS.

1. Start the Bridge with `BRIDGE_PERMISSION_RELAY` unset. `tools/list` shows twelve tools including
   `reply_permission`; the startup log shows the line above.
2. Send a prompt that makes the native session ask for a permission. `get_execution_status` shows
   `waiting_for_permission` and `pending_permission_count: 1`.
3. **Expected today:** `pending_permission` is absent. `reply_permission(reject)` returns `FORBIDDEN`
   / `not_verified`; the native request log shows **no** `POST /permission/*/reply`; the journal has a
   `phase=refused` line and no `phase=attempt` line. The request is still pending: answer it in OpenCode.
4. `reply_permission(allow_once)` returns `FORBIDDEN` / `allow_once_disabled`; the request log shows no
   call at all for it.
5. **Once an incarnation source exists** (not today): `pending_permission` carries `attribution:
   "verified"` and no pattern; `reply_permission(reject)` yields exactly one native reply with body
   `{"reply":"reject"}`, an `attempt` and a `result` journal line, and the execution is then reported
   through ADR-018's `permission_rejection` without the Bridge releasing its guard. Answering the
   request in OpenCode first, then calling: `NOT_FOUND` / `resolved_elsewhere_or_dropped`, no retry.
6. `BRIDGE_PERMISSION_RELAY=off`: eleven tools, no source, no replier.

## Out of scope here

`allow_once`, any policy or allowlist, patterns in `pending_permission`, Claude Code executions, and a
workspace-routed reply (the reply routes by the session's directory, like prompt and abort).
