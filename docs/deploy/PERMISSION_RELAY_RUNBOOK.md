# Permission relay: operator runbook (ADR-024, `reject_only`)

`reply_permission` lets Claude Chat **reject** a pending native OpenCode permission request of an
execution the Bridge started. It can only reject: `allow_once` is not offered (decision D12, exception
(b)) and the policy mode is off (D15 Q1). It is **registered by default**; this page says how to turn
it off, what it does today, and how to check it live. Status: implemented and tested against fakes;
the live acceptance below is **not yet exercised**.

**Operator mode (D21 Q7): until ADR-024 A1 slice S7 is accepted and exercised live, run the Bridge with
`BRIDGE_PERMISSION_RELAY=off`.** The tool is then not registered; the text below describes the code path
and the post-S7 acceptance.

## What it does, in one paragraph

When an execution is `waiting_for_permission`, `get_execution_status` may show a `pending_permission`
object (tool, kind, pattern count, a digest; never a pattern or native text) for the one request the
Bridge can attribute to that execution. `reply_permission {execution_id, permission_id, decision:
"reject"}` re-reads the native request list fresh, checks that request belongs to that execution,
writes an audit line, and sends exactly one native `POST /permission/:id/reply` with `{"reply":"reject"}`.
Every refusal before the audit line sends nothing.

## What can be verified: the incarnation mode (ADR-024 A1 S7, D21)

The Bridge attributes a request to an execution only with incarnation evidence. `BRIDGE_PERMISSION_INCARNATION`
decides, and it is **off by default** (unset or `off`; `observed` turns it on; anything else is a startup
error; it has no effect with `BRIDGE_PERMISSION_RELAY=off`, no tracker is started).

- **off (the default).** No source exists (`docs/P2_PERM_CORE_02_OBSERVATION.md`: none is fabricated). On a
  deployed Bridge **every request stays `unattributed`**: `pending_permission` is absent, only
  `pending_permission_count` is shown, and `reply_permission` answers `FORBIDDEN` with reason `not_verified`,
  no native write, no attempt line. The startup log says so: `permission relay: reject_only; no native
  incarnation source (BRIDGE_PERMISSION_INCARNATION is off) ...`. Answer such a request in OpenCode itself.
- **observed.** The Bridge keeps one subscription to OpenCode's `GET /global/event` and a Bridge-made
  continuity epoch (a random token, replaced on every reconnect, on any dispose event, on a stream end, on an
  undecodable event and after 25 s without an event). It is armed after the first heartbeat of a connection,
  so after every Bridge start and every reconnect **expect about 10 s or more** (UNVERIFIED) in which
  nothing can be verified. A request is attributed only for an execution **submitted while the epoch was
  armed and still the current one**: after a dispose, a reload, a config change, an upgrade or a lost stream
  the earlier executions can never be verified, new ones can. This is continuity of observation, not native
  identity; a window of milliseconds (event latency) and, on a silent network drop, up to about 25 s remains.
  The startup log says `permission relay: reject_only; incarnation=observed ...`.

**Operator note (D21 Q7).** Until S7 has been accepted and exercised live, run with
`BRIDGE_PERMISSION_RELAY=off` (the H5 runbook's stage 1 uses it). S7 has not been run against a live OpenCode.

## Enable, disable

| `BRIDGE_PERMISSION_RELAY` | Result |
|---|---|
| unset or `reject_only` | the tool is registered (twelve tools; thirteen with `create_session`), the OpenCode client is the one replier, the audit writer is installed |
| `off` | nothing is installed; the Bridge keeps its eleven tools |
| `policy` or anything else | the Bridge refuses to start (policy mode is not started) |

**D21 Q7 (operator):** until S7 is accepted and exercised live, the operating setting is `off`
(the table describes the code path, not the instruction).

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

1. After S7 acceptance, start the Bridge with `BRIDGE_PERMISSION_RELAY` unset. `tools/list` shows twelve
   tools including `reply_permission`; the startup log shows the line above. Before S7, D21 Q7 requires
   `off`, so this acceptance is NOT_RUN.
2. Send a prompt that makes the native session ask for a permission. `get_execution_status` shows
   `waiting_for_permission` and `pending_permission_count: 1`.
3. **Expected with the incarnation mode off (the default):** `pending_permission` is absent. `reply_permission(reject)` returns `FORBIDDEN`
   / `not_verified`; the native request log shows **no** `POST /permission/*/reply`; the journal has a
   `phase=refused` line and no `phase=attempt` line. The request is still pending: answer it in OpenCode.
4. `reply_permission(allow_once)` returns `FORBIDDEN` / `allow_once_disabled`; the request log shows no
   call at all for it.
5. **With `BRIDGE_PERMISSION_INCARNATION=observed` and an execution submitted while the epoch is armed**
   (UNVERIFIED live; run it only as the R5b-style acceptance of ADR-024 A1.6): `pending_permission` carries `attribution:
   "verified"` and no pattern; `reply_permission(reject)` yields exactly one native reply with body
   `{"reply":"reject"}`, an `attempt` and a `result` journal line, and the execution is then reported
   through ADR-018's `permission_rejection` without the Bridge releasing its guard. Answering the
   request in OpenCode first, then calling: `NOT_FOUND` / `resolved_elsewhere_or_dropped`, no retry.
6. `BRIDGE_PERMISSION_RELAY=off`: eleven tools, no source, no replier.

## Out of scope here

`allow_once`, any policy or allowlist, patterns in `pending_permission`, Claude Code executions, and a
workspace-routed reply (the reply routes by the session's directory, like prompt and abort).
