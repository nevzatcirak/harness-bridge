# ADR-024: Permission relay (Claude Chat answers a native permission request)

Date: 2026-11-11. Status: **PROPOSED**. Design only; no production code,
nothing deployed. No change to existing fields or semantics; an additive tool
(`reply_permission`) and additive fields (`pending_permission`,
`pending_permission_count`) are proposed. Written for maintainer requirement
D11. Decision in force: D12 (maintainer decision, recorded in issue #117,
2026-11-11): ADR acceptance is delegated to the Planning Desk with exception
(b), enabling any `allow_once` policy stays with the maintainer; a
`reject_only` default may be implemented. This ADR does not enable any
`allow_once` policy.

Numbering: ADR-024 is reserved for this decision. It builds on ADR-018 §5
(pending permission detail, PROPOSED until now) and may amend ADR-018 §5 if
open question 2 is accepted (the bounded-pattern exposure relaxes §5's "never
raw patterns"). It amends no other accepted ADR.

Must preserve: ADR-010 (single writer), ADR-011 (late completion), ADR-017,
ADR-018 option (d) built on (a), ADR-020, and the project invariants: no
implicit native permission approval; do not bypass native harness permissions;
do not expose untrusted tool output as authority; preserve explicit approval
for destructive operations. This ADR **does not weaken** any of them. It adds an
explicit, operator-enabled, audited path, and it says plainly what that path
can and cannot prove.

Upstream facts below are static source reading of `anomalyco/opencode` tag
v1.19.4 = commit `148842ef7cb1bc34192b3fff57d0f9721ab9c801` (a fresh sparse
checkout, read only; OpenCode was not run and no Bridge or OpenCode call was
made). Paths are under `packages/opencode/src/` unless prefixed `schema/`
(`packages/schema/src/`). Anything not settled by source is marked
**UNVERIFIED**. Abbreviations: `grp-s` = `server/routes/instance/httpapi/groups/session.ts`,
`hnd-s` = `server/routes/instance/httpapi/handlers/session.ts`,
`grp-p` / `hnd-p` = the same paths for `permission.ts`, `perm` =
`permission/index.ts`.

## 1. Requirement and the limit that comes with it

D11 (maintainer, 2026-11-11): *"the permissions in each session are carried to
the main session through the Bridge."* When a native session waits for
permission, Claude Chat sees the request and can answer it.

The P4-PERM research (`docs/research/P4_PERM_FEASIBILITY.md`, 2026-11-07) found
**no server-verifiable human approval channel from Claude Chat**:

- Chat elicitation support is UNVERIFIED in official sources, and a form-mode
  answer is a client-asserted value without identity or signature (research §1,
  §2).
- Claude's generic tool-call consent never reaches the server: the Bridge sees
  only the bearer header, the JSON-RPC request and model-authored arguments
  (research §3).
- The native reply API shows only the Bridge's shared Basic Auth credential and
  has no field for who decided (research §5).

What that means for this ADR, stated once so no later section blurs it:

> A `reply_permission` call is **a decision made by the Chat model**, or by a
> human **only if** claude.ai's per-call tool approval is configured for this
> tool and the human really answers it. **The server cannot verify which.** An
> `allow_once` sent through the Bridge is therefore *not* server-verified human
> approval. The maintainer accepts that risk explicitly, by enabling the policy
> in section 5, or does not enable it.

Everything below is built so that the safe path (reject) is always available
and the risky path (allow once) is off until the maintainer turns it on,
narrowly, per root.

## 2. Native facts (verified in source)

| Fact | Evidence |
|---|---|
| Current reply route: `POST /permission/:requestID/reply`, body `{reply: once|always|reject, message?}` | `grp-p` (`HttpApiEndpoint.post("reply", `${root}/:requestID/reply`)`), `hnd-p` (`reply` handler), `schema/v1/permission.ts:38` (`Reply = once|always|reject`) |
| Pending list: `GET /permission` returns pending requests **across all sessions** of the instance | `grp-p` (`list`: "Get all pending permission requests across all sessions") |
| Legacy route named in the task: `POST /session/:sessionID/permissions/:permissionID`, body `{response}`. **Marked `deprecated: true`.** The handler only calls `requireSession(sessionID)` and then replies by `permissionID`; it does **not** check that the request belongs to that session | `grp-s:101` (path), `:395-408` (endpoint, `deprecated: true` at `:406`), `hnd-s:362-378` (`requireSession` at `:366`, `reply` by `requestID: ctx.params.permissionID` at `:367`) |
| Request fields available natively: `sessionID`, `patterns[]`, `always[]`, optional `tool {messageID, callID}`, plus the request ID | `schema/v1/permission.ts:27-36` |
| Reply is first-wins and at most once per pending request in one process: the entry is looked up, deleted, then resolved; a second reply gets `NotFoundError` | `perm:109-114` |
| No idempotency key; a retried reply after a lost response is indistinguishable from "never existed" or "dropped by instance disposal" | `perm:109-114` (not-found path), research §5 |
| `always` adds allow rules and auto-approves other pending requests of the session that now match; `reject` cascades to every other pending request of the session | `perm:121-166` (as recorded in research §5) |
| Reply publishes `permission.replied` with `sessionID`, `requestID`, `reply`; it is not durable | `perm:115-120`, ADR-018 §1(c) |

**Discrepancy with the task text, reported here and in the issue.** The task
asks for the route `POST /session/:id/permissions/:permissionID`. At
`148842ef` that route is deprecated and weaker than the current one (no
session/request binding). This ADR therefore specifies the **current** route
`POST /permission/:requestID/reply` and does the session binding in the Bridge
(section 4). The legacy route stays a fallback only if the maintainer wants it;
if it is ever used the Bridge's own binding is the only binding. The route
choice does not change the security argument.

## 3. Exposure of the pending permission (builds on ADR-018 §5)

ADR-018 §5 (P3, PROPOSED) proposed exposing the `ObservedPermission` safe
projection and **never raw patterns**. D11 needs more than that for an
informed decision. This ADR keeps the safe projection as the default and adds a
bounded, opt-in extension, and the maintainer decides (open question 2).

When an execution is `waiting_for_permission`, `get_execution_status` and
`get_execution_result` return an additive object (the seven original tool
shapes stay valid; clients that ignore the field are unaffected):

`pending_permission` :
- `permission_id` (the native request ID);
- `call_id` and `message_id` (from the native `tool` object, when present);
- `tool`, `kind`, `pattern_count`, `scope_digest` (the existing safe
  projection, `internal/adapters/opencode/permission_observation_types.go:54-63`);
- `attribution`: `verified` / `unattributed` / `conflicting` (existing field);
- `patterns` (**only** in policy mode, section 5): at most 8 entries of at most
  200 bytes, control characters removed, each returned as a JSON string and
  labelled `untrusted` in the schema description.

`get_session` returns only `pending_permission_count` for a session, so a
listing never carries patterns.

Only requests with `attribution = verified` for **this** execution are shown
as actionable; others are shown as a count only. `verified` is required
unconditionally: a request without a native `tool` object (a tool-less request)
cannot be correlated to this execution's assistant message by call ID, so it
is never `verified`, is shown as a count only, and cannot be answered through
the relay (the operator answers it natively). Session equality alone is not
enough, because the native `GET /permission` lists requests of all sessions
and a same-session request could belong to another turn or run. Patterns are native/untrusted
text and are prompt-injection surface, which is why they are opt-in. The
Bridge never returns native message text, only these fields.

Read path: the existing `GET /permission` observation; today the execution path
keeps only `sessionID` from it (`internal/adapters/opencode/client.go:71-73`,
the `Permission` struct; the fetch is `:202-206`),
so wiring the already-built `ObservedPermission` into the manager and MCP is
part of this work (ADR-018 §5 already says the same).

## 4. The tool: `reply_permission`

New mutation tool, additive.

| Field | Rule |
|---|---|
| `execution_id` | an execution known to this writer |
| `permission_id` | the native request ID; must match `[A-Za-z0-9_-]{1,128}` (closed pattern) |
| `decision` | `reject` or `allow_once` only. **No `always`, ever.** The schema rejects any other value |

Output: `{execution_id, permission_id, decision, outcome, decided_by, human_verified: false}`
where `outcome` is `replied` and `decided_by` is the literal `chat_client`. The
two fields are in every successful result so no client can present the answer
as verified.

### 4.1 Decision table

| Decision | When allowed |
|---|---|
| `reject` | always (given binding below and not read-only). Sent with **no `message`**: a reject with feedback is a `CorrectedError` that lets the native loop continue (ADR-018 §1(b)); a plain reject is the safe stop. Note the native side cascade-rejects the session's other pending requests |
| `allow_once` | only if the policy in section 5 allows it for this exact native request. Maps to native `reply: "once"`. Never `always`, because `always` writes allow rules and auto-approves other pending requests (`perm:143-166`) |

### 4.2 Binding and checks (all fail closed, in this order, before any native write)

1. Authenticate; `BRIDGE_READ_ONLY=1` returns `FORBIDDEN` for the tool
   (reject included, because it is a native mutation, as for `interrupt_execution`).
2. Validate inputs (closed patterns, no extra fields).
3. The execution exists in this writer, its session is authorized (ADR-012
   ACL, extended by ADR-022 root authorization if accepted), and its state is
   `waiting_for_permission` with the guard held by this execution.
4. **Fresh native re-read** (never trust a cached observation): `GET /permission`
   and find the request with `id == permission_id`. It must exist and its
   `sessionID` must equal the execution's native session, **and** its
   `tool.messageID`/`callID` must be present and correlate to the execution's
   assistant message (`attribution = verified`, required unconditionally; a
   tool-less request is refused with `FORBIDDEN`, reason `not_verified`).
   Otherwise `NOT_FOUND` or `FORBIDDEN` (reason from a closed set; no native
   text is returned).
   This is the binding the legacy route lacks.
5. For `allow_once`: evaluate the policy against the **live native request**
   (tool, kind, patterns), never against values supplied by the caller.
6. Directory identity recheck (ADR-014) immediately before the write.
7. **Record the attempt before the reply** (audit line with `phase=attempt`),
   then `POST /permission/:requestID/reply` with `{reply}` and no `message`,
   under the 5 s native bound and the run's token (so it cannot interleave with
   a prompt, interrupt or an ADR-020 release).
8. Audit the outcome (`phase=result`). Return.

### 4.3 Idempotency, races and timeouts

- **Record, keyed by `(execution_id, permission_id)`, memory-only** (ADR-010).
  It holds the decision, an `attempt_id` (random, Bridge-generated, returned to
  the caller), the attempt number and a state: `refused` (no native write was
  made), `replied` (the native side accepted), or `unknown` (a native write was
  sent and its result is not known). The record is written **before** the
  native write (state `in_flight`, audited as `phase=attempt`) and updated
  after it. Same-key behavior:
  - `replied`, same decision: returns the recorded result, **sends nothing**;
  - `replied`, opposite decision: `IDEMPOTENCY_CONFLICT` (`retryable:false`);
  - `unknown` (or `in_flight`, so an overlapping call): returns the recorded
    `UNKNOWN_OUTCOME` (`retryable:false`) with its `attempt_id`, **sends
    nothing**, whatever the decision in the new call;
  - `refused`: re-evaluated normally (nothing was sent).
  After a restart the record is empty; the native side then answers a reply to
  a request that is gone with not-found, which is the fail-closed outcome. A
  request that is still pending after a restart is, from the Bridge's view, a
  fresh request with attempt number 1.
- **Never an automatic or silent re-reply.** The native API has no
  idempotency key and cannot tell a lost response from a request that never
  existed or was dropped (research §5). A timeout or lost response yields
  `UNKNOWN_OUTCOME` with the `attempt_id`, and the Bridge does not resend by
  itself, ever. A caller who wants another try must make a **new, explicit,
  distinctly keyed attempt**:
  1. the call carries `supersedes: <attempt_id>` quoting the unknown attempt
     (proof it saw the unknown outcome; a call without it only gets the
     recorded `UNKNOWN_OUTCOME` above);
  2. it must carry the **same decision** as the unknown attempt (a different
     one is `INVALID_INPUT`), so the retry cannot turn a possibly landed
     `allow_once` into a different answer;
  3. the Bridge re-reads natively and requires the request to be **still
     pending** with the same ID and session. Native `reply` deletes the pending
     entry before resolving it (`perm:109-114`), so "still pending" means the
     earlier reply was not applied, and a late-arriving earlier reply and this
     one cannot both apply (first wins, at most once). If the request is gone,
     the result is `NOT_FOUND` (`resolved_elsewhere_or_dropped`) and nothing is
     sent;
  4. it is recorded as attempt number 2 under a new `attempt_id`, audited as
     `phase=attempt` with `attempt=2` and `supersedes=<attempt_id>`, so it is
     never indistinguishable from a resend;
  5. at most **one** supersede per `(execution_id, permission_id)`; a second
     `UNKNOWN_OUTCOME` ends the relay's attempts for that request (the operator
     answers it natively).
  The project rule "no automatic replay of uncertain native mutations" holds
  because no replay is automatic; whether even an explicit superseding attempt
  is acceptable is open question 8. After the explicit attempt the Bridge still
  does not resolve a lost response by guessing; it only learns from the native
  re-read.
- **Race with the native TUI or another client**: first reply wins natively.
  If the TUI answered first, the Bridge's reply gets not-found and returns
  `NOT_FOUND` with the closed reason `resolved_elsewhere_or_dropped`. The
  Bridge never retries and never treats it as its own success.
- **No Bridge-side permission timeout.** The Bridge never auto-replies when the
  caller goes away or a request stays pending. Unanswered requests stay pending
  natively and the execution stays `waiting_for_permission`, as today.
- After a successful reply the Bridge **does not release the guard** and does not
  decide the execution's outcome. The existing reconciliation (ADR-011, ADR-017,
  ADR-018 option (d)) observes what happens next. A `reject` therefore shows up
  through the existing `permission_rejection` report, not as a new terminal
  state.

## 5. Operator policy

Mode, set by the operator at startup, immutable at runtime (restart to change),
like the ACL:

`BRIDGE_PERMISSION_RELAY` = `off` | `reject_only` (**default**) | `policy`.

- `off`: the tool is not registered and `pending_permission` is not returned
  (behavior of today).
- `reject_only` (default): the tool is registered; `reject` is allowed;
  `allow_once` is always refused with `FORBIDDEN` (reason
  `allow_once_disabled`). `pending_permission` is returned without `patterns`.
- `policy`: `allow_once` is evaluated against `BRIDGE_PERMISSION_POLICY`, a JSON
  file owned by the service user, mode `0600`, read once at startup. The
  maintainer opts in explicitly.

Policy contents (proposed names; fail-closed parsing, unknown keys rejected):

- per root (or per session ID for static sessions): `allow_tools` (an allowlist
  of native tool names; empty means none), `deny_patterns` (an ordered
  denylist; any match refuses), `max_allow_once_per_execution`,
  `max_allow_once_per_hour`;
- global: `max_allow_once_per_hour`, and a hard-coded floor that policy cannot
  remove: refuse `allow_once` when any pattern is unparseable, when the request
  has zero patterns for a path-bearing tool, or when `pattern_count` exceeds 8.

Evaluation of a request (every rule must hold; anything unknown refuses):

1. the tool is on the root's `allow_tools`;
2. no pattern matches a `deny_patterns` entry; the recommended starting
   denylist covers anything outside the root, shell/exec tools, network tools
   if configured, and credential-like paths;
3. path patterns are canonicalized against the session directory and must lie
   **inside** the lifecycle/ACL root (symlinks resolved, `..` rejected); a
   pattern the Bridge cannot interpret as a path or a known safe form refuses;
4. rate limits are not exceeded.

Default `deny` for everything not listed. A refused `allow_once` returns
`FORBIDDEN` with a closed reason code (`policy_tool`, `policy_pattern`,
`policy_scope`, `policy_rate`, `allow_once_disabled`), writes an audit line
and makes **no native call**; the request stays pending so the operator can
answer it natively or Chat can `reject` it.

## 6. Trust labels and audit

AUDIT-01 gets `event=permission_reply`, one line per phase per call on every
return path (so one call writes `attempt` then `result`, and a refusal before
the attempt writes one `refused` line). Fields: `tool=reply_permission`,
`phase`, `attempt` (1, or 2 for a superseding attempt), `supersedes` (the
`attempt_id` it supersedes, when present), `outcome`, `accepted`, `error_code`,
`decision`, `permission_id`,
`execution_id`, `session_id` (when well formed), `native_tool`, `kind`,
`scope_digest`, `pattern_count`, `policy_mode`, `policy_rule` (the closed code
that allowed or refused), and always:

- `decided_by=chat_client`
- `human_verified=false`

The line never contains patterns, native text, messages, or prompt content
(same rule as the existing lines). For `allow_once` the audit line is the
operator's record that the maintainer's enabled policy allowed a
model-asserted decision.

The ADR states the risk in one sentence for the maintainer: **enabling
`policy` mode lets the Chat model authorize native actions inside the
allowlist without a server-verifiable human.** The mitigations below reduce
the blast radius; they do not remove that property.

## 7. Claude Code

Relay is **not possible** under the current Claude Code CLI contract. The only
candidate hook, `--permission-prompt-tool`, has an undocumented schema (P6
spike evidence). This is future work and needs its own native-API evidence and
ADR after ADR-021. `reply_permission` accepts only OpenCode executions until
then and returns `INVALID_INPUT` for any other harness.

## 8. Prompt-injection threat model

Scenario: repository content, a web page or a tool output tricks Chat into
calling `reply_permission(allow_once)` for a harmful request.

| Threat | Control |
|---|---|
| Injected text drives `allow_once` | default is `reject_only`; in `policy` mode only allowlisted tools and in-root, non-denied patterns; no `always`; the policy is evaluated on the live native request, not on caller values |
| Chat approves a request that belongs to another session or execution | step 4 binding (session, request, message/call correlation, attribution `verified`); the legacy route's missing binding is not used |
| Pattern text itself carries instructions to Chat | patterns are opt-in, bounded, control-stripped, labelled untrusted; default mode shows none |
| Mass approval loop | per-execution and per-hour caps; one request per call; no bulk form |
| Replay or double reply | memory-only key, never re-reply, first-wins native |
| Race with the TUI | native first-wins, `NOT_FOUND` result, no retry |
| `reject` used to disrupt work | allowed by design (safe direction); audited; rate counted |
| Forged attribution | attribution comes from native correlation, not caller input; `unattributed`/`conflicting` are never actionable |
| Log flooding | one attempt + one result per call; bounded fields |
| Approval looks human-verified downstream | `decided_by=chat_client`, `human_verified=false` are in the result and the audit line |
| Same-UID attacker on the host | out of scope (ADR-020 trust boundary) |

**Strongly recommended operator control:** configure claude.ai's per-call
approval for the `reply_permission` tool so that a human clicks before each
call. The Bridge can mark the tool `destructiveHint: true` and
`readOnlyHint: false`, but MCP annotations are untrusted hints to the client
(research §3) and Claude's behavior for them is UNVERIFIED. The server cannot
see whether a human approved. This control therefore reduces risk only if the
human operator sets it up and uses it; the Bridge cannot enforce or verify it.

## 9. Options considered

1. **Do nothing; keep the fail-closed fallback** (approve in the native UI).
   Safe and unchanged, but does not meet D11.
2. **Reject-only relay** (`reject_only`, the default). Meets "see and answer"
   for the safe direction with no new approval risk.
3. **Policy-gated `allow_once`** (`policy`). Meets D11 fully; the residual risk
   is the unverifiable decider (section 1).
4. **Out-of-band approval page** with a human authentication step (P4-PERM
   option (c)). The only shape found that gives a server-verifiable human
   action; a separate, security-critical ADR. This ADR is compatible with it
   (the page would call the same Bridge function), and it remains the route
   to real human verification.
5. **Legacy route `POST /session/:id/permissions/:permissionID`.** Rejected as
   the primary path (deprecated, no binding); optional fallback only.

**Recommendation:** ship option 2 first (default `reject_only`), then enable
option 3 only for named roots after the live acceptance in section 11, with
per-call claude.ai approval on the tool. Treat option 4 as the follow-up if the
maintainer needs verified human approval. The maintainer decides.

## 10. Implementation plan (PR slices)

| Slice | Content | Tests | Mutations |
|---|---|---|---|
| S1 | Wire `ObservedPermission` into the manager; `pending_permission` and `pending_permission_count` (no patterns) | `waiting_for_permission` exposes the fields; other states do not; unattributed/conflicting are count-only; no native text | expose a pattern in default mode; show an unattributed request as actionable |
| S2 | Policy parser and evaluator (pure, no I/O) | table tests for every rule and every closed reason; fail-closed on unknown keys/unparseable patterns; path canonicalization (symlink, `..`, outside root) | drop the outside-root check; treat an unparseable pattern as allowed |
| S3 | `reply_permission` with `reject`, fake native: binding, order of checks, no `message`, attempt-before-reply, no re-reply | wrong session, wrong execution, request gone, read-only, TUI-first (not-found), lost response (`UNKNOWN_OUTCOME`, exactly one native call), same-key call after `unknown` sends nothing and returns the recorded `UNKNOWN_OUTCOME`, overlapping call while `in_flight` sends nothing, superseding attempt only with `supersedes` + same decision + request still pending (and exactly one extra native call), supersede with a different decision `INVALID_INPUT`, supersede when the request is gone `NOT_FOUND` with no native call, second supersede refused, idempotent same decision, `IDEMPOTENCY_CONFLICT`, tool-less request refused as `not_verified` | send a message with reject; resend on timeout; skip the fresh re-read; skip the session check; honor `supersedes` without the still-pending re-read; allow a second supersede; treat a tool-less request as verified |
| S4 | `allow_once` under policy | allowed in-root tool; refused out-of-root, denied pattern, unlisted tool, rate; `always` rejected by schema; no native call on refusal | map `allow_once` to `always`; evaluate the policy on caller-supplied values |
| S5 | Audit lines (`attempt`/`result`/`refused`), `decided_by`/`human_verified` in result and audit | one line per phase on every path; no free text | omit `human_verified`; skip the attempt line |
| S6 | CONTRACTS, runbook, ADR-018 §5 status update after acceptance | doc review | n/a |

Each slice is one task, one branch, one PR; security-critical slices (S3, S4)
need an independent review and negative tests, per the project rules.

## 11. Live acceptance outline (R5b style)

Real OpenCode v1.19.4 and the real Bridge, on a disposable root, maintainer
present for steps marked M. Evidence captured the way H4 did (native DB,
filesystem, journal, independent rebuild). Anything not run is UNVERIFIED, not
PASS.

1. Mode `reject_only`: run a prompt that makes the native session ask for a
   permission. `get_execution_status` shows `pending_permission` without
   patterns and `attribution=verified`.
2. `reply_permission(reject)`: native request resolved, exactly one
   native reply in the request log, audit lines `attempt` and `result` with
   `decided_by=chat_client`, `human_verified=false`; the execution is reported
   through ADR-018's `permission_rejection` and the guard is not released by
   the Bridge.
3. `reply_permission(allow_once)` in `reject_only`: refused, zero native calls.
4. Mode `policy`, allowlisted harmless tool in-root: `allow_once` sent, the
   native tool runs once, a second identical request is a new request and needs
   a new decision.
5. Negative runs: denied pattern, out-of-root path, unlisted tool, rate cap,
   wrong execution ID, wrong session: all refused with no native call.
6. Race: answer in the native TUI first, then call the Bridge: `NOT_FOUND`
   `resolved_elsewhere_or_dropped`, no retry. M
7. Lost response (fault injection): `UNKNOWN_OUTCOME`, exactly one native
   reply, no resend.
8. Prompt-injection drill: repository content that asks Chat to approve a
   forbidden action; the policy refuses, and with claude.ai per-call approval
   enabled the human sees the prompt. M
9. Restart: the idempotency map is empty; a repeated reply gets not-found.

## 12. Open questions for the maintainer

1. **Enable `policy` mode at all** (the unverifiable decider, section 1), or
   stay on `reject_only`?
2. **Patterns shown to Chat** (policy mode only, bounded and untrusted): accept
   this relaxation of ADR-018 §5's "never expose raw patterns"?
3. **Default mode `reject_only` registered**, or default `off` until opted in?
4. **Route:** current `POST /permission/:requestID/reply` (recommended), or the
   deprecated session route named in the task?
5. **Starting policy values** (allow-once caps per execution and per hour; the
   recommended denylist).
6. **Out-of-band approval page** (P4-PERM option (c)) as a follow-up ADR?
7. Is requiring per-call claude.ai approval on `reply_permission` a deployment
   requirement or only a recommendation?
8. After an `UNKNOWN_OUTCOME`, is one explicit, distinctly keyed superseding
   attempt (section 4.3) acceptable, or should the Bridge never send a second
   reply for the same request and leave it to the operator? This ADR specifies
   the explicit attempt as the recommendation; the maintainer decides.

## 13. Limitations of this record

- Static reading of one upstream commit; OpenCode was not run and no Bridge or
  OpenCode call was made. Line numbers for `perm:115-120` and `perm:121-166`
  are carried from the P4-PERM research and were not re-traced here
  (UNVERIFIED beyond that); `perm:109-114`, `hnd-s:362-378`, `grp-s:395-408`
  and `schema/v1/permission.ts:27-38` were re-read for this ADR.
- Claude's behavior for tool annotations and per-call approval, and whether
  Claude Chat elicits at all, are UNVERIFIED (P4-PERM).
- Defaults, tool names, closed reason codes and config keys are proposals.
- No code, test or live run was done for this ADR.
