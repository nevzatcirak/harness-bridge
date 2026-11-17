# ADR-024: Permission relay (Claude Chat answers a native permission request)

Date: 2026-11-11. Status: **ACCEPTED (per D12, 2026-11-12, planner task #205)**.
Design; implementation proceeds in the slices of section 10 (S1 first); nothing
deployed. No change to existing fields or semantics. **The whole
additive surface, listed once:** the tool `reply_permission` (input `execution_id`,
`permission_id`, `decision`, optional `supersedes`; success output `outcome`,
`attempt_id`, `attempt`, `decided_by`, `human_verified`); three optional fields
on the shared error object, used only by this tool (`attempt_id`, `attempt`,
`reason`, section 4); the status/result fields `pending_permission` and
`pending_permission_count` (section 3); the configuration `BRIDGE_PERMISSION_RELAY`
and `BRIDGE_PERMISSION_POLICY` (section 5); and the AUDIT-01 event
`permission_reply` with its fields (section 6). Nothing else changes. Written for
maintainer requirement D11. Decision in force: D12 (maintainer decision, recorded in issue #117,
2026-11-11): ADR acceptance is delegated to the Planning Desk with exception
(b), enabling any `allow_once` policy stays with the maintainer; a
`reject_only` default may be implemented. This ADR does not enable any
`allow_once` policy.

**Amendment A1 (PROPOSED, 2026-11-16, task #315):** an incarnation source for production attribution is
proposed in the section "Amendment A1: Incarnation source" before section 13; it changes nothing until the
maintainer accepts it (D19).

**Acceptance note (2026-11-12, planner task #205).** The Planning Desk accepted
this ADR under decision D12 (issue #117): PR #162 was merged (`479e917`) after
independent reviews #179 (B) and #197 (A), both PASS-WITH-NITS with no open
P0-P2, and the `reject_only` default conforms to D11. Acceptance covers the
**`reject_only` design only**. Open questions 1-8 stay with the maintainer and are
not decided by this acceptance. D12 exception (b), enabling any `allow_once`
policy, stays with the maintainer: S2 and S4 stay gated on open question 1. S3
and S5 additionally depend on the open ROADMAP item `:499-502` (Mutation Gate
integration and the attempt writer), on open question 8 (superseding attempt)
and on open question 3 (registration default); this task does **not** start them.
The text nits of review #197 are applied in this revision (text only: no decision
changed, no option added or removed).

**Update (D15, issue #117, 2026-11-12; task #282).** The maintainer answered open
questions 1, 3, 4, 7 and 8 (section 12, "Decided (D15)") and deferred 2, 5 and 6 to
the backlog. S3's gating items are resolved: the attempt writer exists (PR #275,
`internal/audit`, as built below), question 3 (registration default) and question 8
(superseding attempt) are answered. S2 and S4 stay **not started: policy mode OFF
(D15 Q1)**; D12 exception (b) is unchanged.

Numbering: ADR-024 is reserved for this decision. It builds on ADR-018 §5
(pending permission detail, PROPOSED until now) and may amend ADR-018 §5 if
open question 2 is accepted (the bounded-pattern exposure relaxes §5's "never
raw patterns"). It amends no other accepted ADR.

Must preserve: ADR-010 (single writer), ADR-011 (late completion), ADR-017,
ADR-018 option (d) built on (a), ADR-020, and the project invariants
(AGENTS.md): single-writer audit and Mutation Gate boundaries; no
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
| Request fields available natively: the request ID (`id`, which must start with `per`, `schema/v1/permission.ts:10`), `sessionID`, `permission` (the permission kind, a string), `patterns[]`, `metadata`, `always[]`, optional `tool {messageID, callID}`. The Bridge's observer maps `permission` to one of 11 known kinds (`read`, `edit`, `write`, `bash`, `external_directory`, `doom_loop`, `webfetch`, `websearch`, `task`, `glob`, `grep`) and everything else to `unrecognized` (`internal/adapters/opencode/permission_observation_codec.go:180-187`) | `schema/v1/permission.ts:10,27-36` |
| A malformed request ID is rejected by the native route with `BadRequest` (400) | the ID schema `isStartsWith("per")` (`schema/v1/permission.ts:10`), the `reply` endpoint declaring `HttpApiError.BadRequest` (`grp-p`), and the schema-error layer that turns a decode failure into a 400 for non-`/api/` paths (`middleware/schema-error.ts`); whether that layer is applied to the permission group was **not traced** (UNVERIFIED) |
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
part of this work (ADR-018 §5 already says the same). The read goes through the
`PermissionObserver` (`internal/adapters/opencode/permission_observation.go:56,72`),
not through a bare `GET /permission`: `attribution = verified` needs the
observer's correlation, which also reads the assistant and parent user messages
(a request without a `tool` object stays `unattributed`, reason
`correlation_missing`, `:282`), and it runs inside the observer's own bound
(`PermissionObservationConfig.Timeout`, default and maximum 5 s for the whole
call including those reads). It uses the **session's own routing**: the client
that observes that session (`Client.WithDirectory`,
`internal/adapters/opencode/client.go:115-116`, plus the workspace target for a
workspace-bound session), because the native `GET /permission` and the reply
route run in the request's instance (`directory`/`workspace` query fields) and a
call routed to the wrong instance simply finds nothing.

## 4. The tool: `reply_permission`

New mutation tool, additive.

| Field | Rule |
|---|---|
| `execution_id` | an execution known to this writer |
| `permission_id` | the native request ID; must match `^per[A-Za-z0-9_-]{1,125}$` (closed pattern; native IDs start with `per`, `schema/v1/permission.ts:10`). A value the native route still rejects with 400 maps to `UPSTREAM_FAILURE`, reason `native_rejected_id`, with no native state change |
| `decision` | `reject` or `allow_once` only. **No `always`, ever.** The schema rejects any other value |
| `supersedes` | **optional.** The `attempt_id` of an earlier attempt of this same `(execution_id, permission_id)` whose outcome was `UNKNOWN_OUTCOME`; closed opaque pattern `[A-Za-z0-9_-]{22}` (the Bridge generates a 22-character base64url value from 16 random bytes). Honored only in the one state described in 4.3; otherwise refused with a closed reason |

The input object keeps `additionalProperties: false`: these four properties
(`execution_id`, `permission_id`, `decision`, `supersedes`) are the whole input,
and any other field is refused. `supersedes` is the only optional one.

Output (success): `{execution_id, permission_id, decision, outcome, attempt_id,
attempt, decided_by, human_verified: false}` where `outcome` is `replied`,
`attempt_id` is the 22-character opaque ID of this attempt, `attempt` is `1` or
`2` (2 only for a superseding attempt), and `decided_by` is the literal
`chat_client`. The two last fields are in every successful result so no client
can present the answer as verified.

**The `UNKNOWN_OUTCOME` response carries the token too.** The shared error
object of CONTRACTS (`code`, `message`, `retryable`, optional `execution_id`,
`docs/CONTRACTS.md:34`) gets three **additive optional** fields, used only by
`reply_permission`: `attempt_id` and `attempt` (only on `UNKNOWN_OUTCOME`), and
`reason`, a **closed enum** that says why a refusal happened. A caller always has
the value to quote in `supersedes`, whether the outcome was the original
`UNKNOWN_OUTCOME` or the recorded one returned for a same-key call (4.3). No
error carries native text. The shared error definition stays valid for every
other tool because the new fields are optional.

`reason` values (closed; any other value is a defect): `not_verified`,
`unrecognized_kind`, `allow_once_disabled`, `policy_tool`, `policy_pattern`,
`policy_scope`, `policy_rate`, `attempt_in_flight`, `already_replied`,
`nothing_to_supersede`, `supersedes_mismatch`, `supersede_limit`,
`supersede_allow_once_not_offered`, `resolved_elsewhere_or_dropped`,
`audit_unavailable`, `native_rejected_id`. Each appears under the code named
where it is introduced; `reason` never replaces `code`.

`process_start` (audit, section 6) is the Bridge process start time, RFC 3339 in
UTC with second precision (for example `2026-11-11T10:50:56Z`), read once at
startup.

### 4.1 Decision table

| Decision | When allowed |
|---|---|
| `reject` | always (given binding below and not read-only). Sent with **no `message`**: a reject with feedback is a `CorrectedError` that lets the native loop continue (ADR-018 §1(b)); a plain reject is the safe stop. Note the native side cascade-rejects the session's other pending requests |
| `allow_once` | only if the policy in section 5 allows it for this exact native request. Maps to native `reply: "once"`. Never `always`, because `always` writes allow rules and auto-approves other pending requests (`perm:143-166`) |

### 4.2 Binding and checks (all fail closed, in this order, before any native write)

The refusals that need no native read come first; the native permission re-read
(step 5) comes only after them; every refusal before step 8 makes **no native
write**. "Zero native calls" is true only for refusals **before step 3**: step 3's
session authorization is the existing `authorized` check, which reads the session
natively (`GET /session/:id`) and checks the workspace (`internal/mcp/server.go:57-70`,
`:142-153`). Refusals at steps 4 and later therefore make no permission read
and no native write, but are not "zero native calls".

1. Authenticate; `BRIDGE_READ_ONLY=1` returns `FORBIDDEN` for the tool
   (reject included, because it is a native mutation, as for `interrupt_execution`).
   Implementation requirement (review #197): `reply_permission` must be added to
   `mutating()` (`internal/mcp/audit.go:31`), which gates both the read-only
   refusal (`server.go:102`) and the audit middleware; a tool missing there would
   be neither blocked in read-only mode nor audited. S3 test: refused under
   `BRIDGE_READ_ONLY` with no native read; mutation: omit it from `mutating()`.
2. Validate inputs (closed patterns, no extra fields) and refuse `allow_once`
   while the mode is `reject_only` (`FORBIDDEN`, reason `allow_once_disabled`):
   this needs only configuration and input, so it makes **zero native calls**.
3. The execution exists in this writer, its session is authorized (ADR-012
   ACL, extended by ADR-022 root authorization if accepted), and its state is
   `waiting_for_permission` with the guard held by this execution.
4. **In-memory refusals (no permission read, no native write):** the same-key
   rules and the `supersedes` rule 0 of 4.3, which read only the Bridge's own
   record (step 3's session read has already happened).
5. **Fresh native re-read** (never trust a cached observation) through the
   `PermissionObserver` of that session, with the session's own directory and
   workspace routing (section 3), inside the observer's 5 s bound. The request
   with `id == permission_id` must exist and its `sessionID` must equal the
   execution's native session, **and** its `tool.messageID`/`callID` must be
   present and correlate to the execution's assistant message (`attribution =
   verified`, required unconditionally; a tool-less request is refused with
   `FORBIDDEN`, reason `not_verified`). Mapping (a proposal, closed set, no native
   text): request absent on a first attempt → `NOT_FOUND`, reason
   `resolved_elsewhere_or_dropped`; request of another session, `unattributed` or
   `conflicting` attribution → `FORBIDDEN`, reason `not_verified`; observer
   unavailable or timed out → `UPSTREAM_FAILURE` or `UPSTREAM_TIMEOUT` with no
   `reason`. Refusals raised by the SDK's input validation carry no `reason`. This
   is the binding the legacy route lacks.
6. For `allow_once`: evaluate the policy against the **live native request**
   (observer `Kind`, tool, patterns), never against values supplied by the
   caller. A `Kind` of `unrecognized` is always refused (`FORBIDDEN`, reason
   `unrecognized_kind`).
7. Directory identity recheck (ADR-014) immediately before the write.
8. **Write the attempt record, then, only if that succeeded, send the reply.**
   The attempt record is the AUDIT-01 line `event=permission_reply`,
   `phase=attempt` (and, where the Mutation Gate provides one, its durable
   witness). It must be written **before** the native call and the writer must
   **report failure**. **If the record cannot be written or the witness is
   unavailable, there is no native write:** the call returns a typed refusal
   (`UPSTREAM_FAILURE`, reason `audit_unavailable`, `retryable: true`; CONTRACTS
   has no local-failure code, so this is the nearest existing one and a
   proposal), the in-memory record is `refused`, and nothing is sent. Only then
   `POST /permission/:requestID/reply` with `{reply}` and no `message`, under the
   5 s native bound and the run's token (so it cannot interleave with a prompt,
   interrupt or an ADR-020 release). **Blocking behaviour (review #277 P3-2; S3
   recorded it, S5 decided it):** the attempt write is bounded by the call's context.
   A journal that hangs past the bound latches the writer and the call refuses with
   `audit_unavailable`; a canceled caller refuses the call without latching. Reason:
   an unbounded write would hold the run token and a reconcile slot (at most four)
   for as long as the journal hangs, starving every execution's reconciliation, while
   the journal's other users are no worse off. The cost is stated: the line may reach
   the journal after the call refused, leaving an `attempt` line with no native write,
   which over-reports an attempt and never hides one; the latch stops every later call.
9. Write the outcome record (`phase=result`) and return. If the **result**
   record cannot be written after the native call happened, the call still
   returns the real outcome (it cannot be unsent), the in-memory record is kept,
   the failure is logged loudly, and **every later `reply_permission` call is
   refused with `audit_unavailable` until the writer recovers**, because a
   missing durable witness blocks further permission mutations. **Open (review
   #197):** how recovery is detected, whether the block is process-wide (as
   written) and whether `reject` and same-key replays that send nothing are still
   served are **not specified here**; S5 designs them with the attempt writer.

**This is a new ordering and a new requirement, not a description of today's
code.** AUDIT-01 is written **after** the call, by a deferred middleware, through
`log/slog`, which cannot report failure (`internal/mcp/audit.go:79-92`,
`slog.Info` at `:160`). Step 8 therefore needs an attempt writer that reports
failure (a synchronous record, or a Mutation Gate witness); an implementation
that logs and continues would let an `allow_once` or a lost reply happen with no
`phase=attempt` record and break section 6 and the accounting of 4.3. It maps to
ROADMAP `:207-211` ("Audit unavailability or missing required durable witness
blocks permission mutations"; "a future contract requirement, not a new Audit
Journal record type or Mutation Gate bypass") and to the open ROADMAP item
`:499-502` (the minimal permission decision/attempt audit contract and Mutation
Gate integration). This ADR uses the existing AUDIT-01 line shape as the record
and defines no new journal record type; the Mutation Gate integration stays that
open item and is a prerequisite of S3 and S5.

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
    `UNKNOWN_OUTCOME` (`retryable:false`) with its `attempt_id` and `attempt`
    (the error fields of section 4), **sends nothing**, whatever the decision in
    the new call, unless the call is a valid superseding attempt (below, only
    from state `unknown`);
  - `refused`: re-evaluated normally (nothing was sent).
  After a restart the record is empty; the native side then answers a reply to
  a request that is gone with not-found, which is the fail-closed outcome.
  **Documented residual (post-restart; a stated residual, not a chosen
  design):** a request that is still pending after a restart is, from the
  Bridge's view, a fresh request with attempt number 1, with no `supersedes` and
  no attempt-2 accounting, so a caller can send another reply for a request
  whose earlier reply may have been in flight when the Bridge stopped. ROADMAP
  `:205` says missing IDs do not mean non-delivery; here the only evidence is not the missing Bridge record but the **native** fact
  that the request is still pending (a delivered reply deletes it first), which
  shows the earlier reply was **not applied yet**, not that it was never delivered
  (it may still land; first-wins makes that harmless). The residual is
  stated, not decided, and the operator-only alternative is open question 8, for
  three reasons: (a) native
  first-wins at-most-once (`perm:109-114`) means the earlier reply and the new
  one cannot both apply, so the worst case is that the answer that lands is the
  one of the two, never a double effect; (b) the Bridge restart is itself a
  fenced operator action (ADR-010), so it is operator-visible, and the new
  attempt is checked by the same native still-pending re-read and the same
  policy as any attempt 1; (c) the audit line carries a `process_start` field
  (the Bridge process start time), so an auditor can see that attempts of one
  `(execution_id, permission_id)` straddle a restart even though the Bridge
  keeps no memory of them. Routing every post-restart pending request through
  an operator-only path is the stricter alternative and is folded into open
  question 8; this ADR does not choose either.
- **Never an automatic or silent re-reply.** The native API has no
  idempotency key and cannot tell a lost response from a request that never
  existed or was dropped (research §5). A timeout or lost response yields
  `UNKNOWN_OUTCOME` with the `attempt_id`, and the Bridge does not resend by
  itself, ever. A caller who wants another try must make a **new, explicit,
  distinctly keyed attempt**:
  0. **`supersedes` is honored only when the record's state is `unknown`.**
     In every other state it is refused **before any native call**, with a
     closed reason: `in_flight` → `SESSION_BUSY`, reason `attempt_in_flight`,
     `retryable: true` (the input is valid; the call raced a first attempt that
     has not settled, which is what CONTRACTS uses `SESSION_BUSY` for; try again
     once it has settled; this stops two concurrent native replies);
     `replied` → `IDEMPOTENCY_CONFLICT`, reason
     `already_replied`; `refused` → `INVALID_INPUT`, reason `nothing_to_supersede`
     (a refused call sent nothing, so retry it normally without `supersedes`);
     no record → `INVALID_INPUT`, reason `nothing_to_supersede`; a value that is
     not the record's current `attempt_id` → `INVALID_INPUT`, reason
     `supersedes_mismatch`; a second supersede (the record already has attempt 2)
     → `FORBIDDEN`, reason `supersede_limit`. None of these reasons carries
     native text.
  0a. **In this proposal a superseding attempt is offered for `reject` only.**
     A resent `reject` cannot create an approval. For an `allow_once` whose
     outcome is `unknown`, a `supersedes` call is refused before any native call
     with `FORBIDDEN`, reason `supersede_allow_once_not_offered`, and the operator
     answers natively. Offering an `allow_once` supersede is exactly what open
     question 8 asks the maintainer, and stays behind that answer.
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
  of observer kinds, section 2; empty means none; `unrecognized` is never
  listable), `deny_patterns` (an ordered
  denylist; any match refuses), `max_allow_once_per_execution`,
  `max_allow_once_per_hour`;
- global: `max_allow_once_per_hour`, and a hard-coded floor that policy cannot
  remove: refuse `allow_once` when any pattern is unparseable, when the request
  has zero patterns for a path-bearing tool, or when `pattern_count` exceeds 8.

Evaluation of a request (every rule must hold; anything unknown refuses):

1. the request's `Kind` is on the root's `allow_tools`;
2. no pattern matches a `deny_patterns` entry; the recommended starting
   denylist covers anything outside the root, shell/exec tools, network tools
   if configured, and credential-like paths;
3. path patterns are canonicalized against the session directory and must lie
   **inside** the lifecycle/ACL root (symlinks resolved, `..` rejected); a
   pattern the Bridge cannot interpret as a path or a known safe form refuses;
4. rate limits are not exceeded.

Default `deny` for everything not listed. A refused `allow_once` returns
`FORBIDDEN` with a closed reason code (`policy_tool`, `policy_pattern`,
`policy_scope`, `policy_rate`, `unrecognized_kind`, `allow_once_disabled`),
writes an audit line and makes **no native write**: `allow_once_disabled` is
refused before any native call (step 2 of 4.2), but the policy rules need the
live request, so the native read of step 5 has already happened. The request
stays pending so the operator can answer it natively or Chat can `reject` it.
`allow_tools` is matched against the observer's `Kind` (the 11 known kinds, section
2); a request of kind `unrecognized` is never allowed, whatever the allowlist.

## 6. Trust labels and audit

AUDIT-01 gets `event=permission_reply`, one line per phase per call on every
return path (so one call writes `attempt` then `result`, and a refusal before
the attempt writes one `refused` line). The `attempt` line is the attempt record
of step 8 in 4.2: it is written before the native call, a failure to write it
means no native write, and it is a new ordering relative to today's after-the-call
AUDIT-01 middleware. Fields: `tool=reply_permission`,
`phase`, `attempt` (1, or 2 for a superseding attempt), `attempt_id`, `supersedes` (the
`attempt_id` it supersedes, when present), `outcome`, `accepted`, `error_code`,
`decision`, `process_start`, `permission_id`,
`execution_id`, `session_id` (when well formed), `native_tool`, `kind`,
`scope_digest`, `pattern_count`, `policy_mode`, `policy_rule` (the closed code
that allowed or refused), `reason` (the closed enum of section 4, when a refusal),
and always:

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

### 9.1 Roadmap conformance

Where this ADR meets, or departs from, the rules recorded in `docs/ROADMAP.md`
and the P4-PERM research. A "deviation: yes" is **not resolved here**: it is a
maintainer question and stays open. D12 exception (c) is "any ADR whose
recommendation deviates from the **D-items**"; deviations from ROADMAP rules are
not D-items (D11 itself is the D-item that relaxes `:183-184`), so they are
reported to the maintainer through the open questions rather than by (c).

| Rule | ADR-024 behavior | Deviation |
|---|---|---|
| ROADMAP `:183-184`: "Accept only a verified explicit human allow-once or deny" | A relayed decision is **model-asserted**: `decided_by=chat_client`, `human_verified=false`; default `reject_only`, `allow_once` off until the maintainer opts in (D11, D12 (b)) | **Yes** (by D11's own premise); stated in section 1, not resolved |
| ROADMAP `:204`: "Do not resend allow, refund consent, release guards or claim success without proof" | No automatic resend; an explicit `supersedes` attempt, same decision, still-pending proof, at most one, **for `reject` only**; no `allow_once` supersede; the relay never releases a guard or claims success | **No** for `allow_once` (not offered); **arguably yes** for `reject` (a resent reject cannot approve); open question 8 |
| ROADMAP `:205`: after a restart "fail closed, fence/reconcile ... missing IDs do not mean non-delivery" | Record is memory-only and nothing is restored; a still-pending request after a restart is a fresh attempt 1, with native still-pending as the only non-delivery evidence | **Residual**, stated not chosen; open question 8 |
| ROADMAP `:207-211`: audit unavailability or a missing durable witness blocks permission mutations | Step 8 of 4.2: attempt record unwritable → no native write; result record unwritable → later calls refused | **No** (new ordering required, 4.2) |
| ROADMAP `:499-502` (open): minimal permission decision/attempt audit contract and Mutation Gate integration | Uses the AUDIT-01 line shape; defines no new journal record type; the integration remains the open item | Prerequisite of S3/S5, not resolved |
| P4-PERM research §5: "cannot resolve a lost reply without re-issuing it, which the security invariants forbid" | The Bridge does not resolve a lost reply by guessing; it learns only from the native still-pending re-read; explicit reject re-issue only | **Arguably yes**; open question 8 |

## 10. Implementation plan (PR slices)

| Slice | Content | Tests | Mutations |
|---|---|---|---|
| S1 | Wire `ObservedPermission` into the manager; `pending_permission` and `pending_permission_count` (no patterns) | `waiting_for_permission` exposes the fields; other states do not; unattributed/conflicting are count-only; no native text | expose a pattern in default mode; show an unattributed request as actionable |
| S2 (**not started: policy mode OFF (D15 Q1)**; D12 exception (b)) | Policy parser and evaluator (pure, no I/O) | table tests for every rule and every closed reason; fail-closed on unknown keys/unparseable patterns; path canonicalization (symlink, `..`, outside root) | drop the outside-root check; treat an unparseable pattern as allowed |
| S3 | `reply_permission` with `reject`, fake native: binding, order of checks, no `message`, attempt-before-reply, no re-reply | wrong session, wrong execution, request gone, read-only, TUI-first (not-found), lost response (`UNKNOWN_OUTCOME`, exactly one native call), same-key call after `unknown` sends nothing and returns the recorded `UNKNOWN_OUTCOME`, overlapping call while `in_flight` sends nothing, superseding attempt only with `supersedes` + same decision + request still pending (and exactly one extra native call), supersede with a different decision `INVALID_INPUT`, supersede when the request is gone `NOT_FOUND` with no native write (the still-pending re-read is a read), second supersede refused, idempotent same decision, `IDEMPOTENCY_CONFLICT`, tool-less request refused as `not_verified`. **Contract tests:** the input schema accepts exactly `execution_id`, `permission_id`, `decision` and the optional `supersedes` (closed `[A-Za-z0-9_-]{22}` pattern) and rejects any other field or a malformed `supersedes`; the success result always carries `attempt_id`, `attempt`, `decided_by=chat_client`, `human_verified=false`; the `UNKNOWN_OUTCOME` envelope carries `attempt_id` and `attempt`, and no other error code does; `supersedes` is refused with the closed reason and **no native call** from `in_flight` (`SESSION_BUSY`, `attempt_in_flight`, retryable), for an `allow_once` record (`supersede_allow_once_not_offered`), `replied` (`already_replied`), `refused` or no record (`nothing_to_supersede`), a wrong value (`supersedes_mismatch`), and a second supersede (`supersede_limit`); `supersedes` is honored only from `unknown`; the audit line of a post-restart attempt 1 carries `process_start` (RFC 3339 UTC); `reason` is always one of the closed enum; **attempt-record failure: when the `phase=attempt` write fails (writer error or unavailable witness) there are zero native writes, the call returns `UPSTREAM_FAILURE`/`audit_unavailable`, and the record is `refused`; when the `phase=result` write fails after the native call the real outcome is still returned and every later call is refused with `audit_unavailable`**; `permission_id` must match `^per[A-Za-z0-9_-]{1,125}$` and a native 400 maps to `UPSTREAM_FAILURE`; the re-read and the reply use the session's directory/workspace routing; `allow_once_disabled` (step 2) makes zero native calls, and the same-key and `supersedes` rule 0 refusals make no permission read and no native write; `reply_permission` is refused under `BRIDGE_READ_ONLY` with no native read (it is in `mutating()`) | send a message with reject; resend on timeout; skip the fresh re-read; skip the session check; honor `supersedes` without the still-pending re-read; allow a second supersede; treat a tool-less request as verified; **continue to the native write when the attempt record write fails; honor `supersedes` while the state is `in_flight` or `replied`; offer `supersedes` for `allow_once`; perform the re-read before the in-memory refusals; omit `reply_permission` from `mutating()`; route the re-read with the default directory instead of the session's; omit `attempt_id` from the `UNKNOWN_OUTCOME` envelope or the success result; accept an unknown input field or a malformed `supersedes`; skip the `supersedes_mismatch` check** |
| S4 (**not started: policy mode OFF (D15 Q1)**; D12 exception (b)) | `allow_once` under policy | allowed in-root tool; refused out-of-root, denied pattern, unlisted tool, rate; `always` rejected by schema; no native write on refusal (the policy rules need the live request, so the native read has happened; `allow_once_disabled` makes no native call at all) | map `allow_once` to `always`; evaluate the policy on caller-supplied values |
| S5 | Audit lines (`attempt`/`result`/`refused`), `decided_by`/`human_verified` in result and audit; **an attempt writer that reports failure (synchronous record or Mutation Gate witness)** | `reply_permission` is in `mutating()` so the audit middleware wraps it; one line per phase on every path; no free text; the attempt line is written before the native call and its failure blocks the write; the result-line failure blocks later calls | omit `human_verified`; skip the attempt line; log-and-continue on a failed attempt write |
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
3. `reply_permission(allow_once)` in `reject_only`: refused with `allow_once_disabled` at step 2, **zero native calls** (the request log shows none).
4. Mode `policy`, allowlisted harmless tool in-root: `allow_once` sent, the
   native tool runs once, a second identical request is a new request and needs
   a new decision.
5. Negative runs: an unknown execution ID or a session outside the ACL are
   refused before any native read of the request (the ACL check itself reads
   `GET /session/:id`); a request of another session is refused at step 5 with
   **no native write**; denied pattern, out-of-root path, unlisted tool, `unrecognized`
   kind and rate cap are refused with **no native write** (the request log shows
   only the native read of step 5).
6. Race: answer in the native TUI first, then call the Bridge: `NOT_FOUND`
   `resolved_elsewhere_or_dropped`, no retry. M
7. Lost response (fault injection): `UNKNOWN_OUTCOME`, exactly one native
   reply, no resend. Attempt-record failure (the audit sink made unavailable):
   zero native writes and `audit_unavailable`.
8. Prompt-injection drill: repository content that asks Chat to approve a
   forbidden action; the policy refuses, and with claude.ai per-call approval
   enabled the human sees the prompt. M
9. Restart: the idempotency map is empty; a repeated reply gets not-found.

## 12. Open questions for the maintainer

Questions 1, 3, 4, 7 and 8 were answered by the maintainer in decision **D15**
(issue #117, 2026-11-12) and are recorded below as "Decided (D15)"; questions 2, 5
and 6 are deferred to the backlog. Nothing else in this ADR changes.

1. **Enable `policy` mode at all** (the unverifiable decider, section 1), or
   stay on `reject_only`?
   **Decided (D15, issue #117): policy mode stays OFF; `reject_only`.** S2 and S4
   (policy parser, `allow_once`) are **not started: policy mode OFF (D15 Q1)**;
   D12 exception (b) is unchanged.
2. **Patterns shown to Chat** (policy mode only, bounded and untrusted): accept
   this relaxation of ADR-018 section 5's "never expose raw patterns"?
   **Deferred / backlog (D15):** policy mode only, so it follows question 1.
3. **Default mode `reject_only` registered**, or default `off` until opted in?
   **Decided (D15, issue #117): `reject_only` is registered as the default.**
   Registration in the production server lands with S5 (the audit lines); S3
   builds the tool and does not register it (section "As built (S3)").
4. **Route:** current `POST /permission/:requestID/reply` (recommended), or the
   deprecated session route named in the task?
   **Decided (D15, issue #117): `POST /permission/:requestID/reply`.**
5. **Starting policy values** (allow-once caps per execution and per hour; the
   recommended denylist).
   **Deferred / backlog (D15):** policy mode only.
6. **Out-of-band approval page** (P4-PERM option (c)) as a follow-up ADR?
   **Deferred / backlog (D15).**
7. Is requiring per-call claude.ai approval on `reply_permission` a deployment
   requirement or only a recommendation?
   **Decided (D15, issue #117): required if policy mode is ever enabled;
   recommended for `reject`.**
8. After an `UNKNOWN_OUTCOME`, is one explicit, distinctly keyed superseding
   attempt (section 4.3) acceptable, or should the Bridge never send a second
   reply for the same request and leave it to the operator? This ADR specifies
   the explicit attempt as the recommendation for `reject` only (`allow_once`
   supersede is not offered, 4.3); the maintainer decides both. The same
   question covers the post-restart path of 4.3: keep the documented residual
   (a still-pending request after a restart is a fresh attempt 1), or route
   every post-restart pending request through an operator-only path.
   **Decided (D15, issue #117): one explicit, distinctly keyed superseding
   `reject` after `UNKNOWN_OUTCOME` is allowed (section 4.3); the post-restart
   path keeps the documented residual.** An `allow_once` supersede stays not
   offered.

## As built (attempt writer), 2026-11-14

Facts only; the open items of review #197 stay open. The D15 prerequisite of S3/S5 exists in
`internal/audit` (task #274), **not wired into any tool** and not in `mutating()`.

- **What it is.** `audit.Writer.Write` builds one AUDIT-01-shaped line (`event=permission_reply`,
  `phase=attempt`, then the allowlisted attributes of section 6 in a fixed order, then
  `decided_by=chat_client`, `human_verified=false`) and hands it to the process default `slog`
  handler. It returns the handler's error. It defines no journal record type, changes no existing
  line and does not touch the deferred AUDIT-01 middleware (`internal/mcp/audit.go`).
- **Destination and lock.** It uses `slog.Default().Handler()` at call time, the same destination
  the existing lines use. With the dashboard tee installed (#251) the line goes through the tee's
  journal branch and the `syncWriter` the `log` package shares, so bytes and ordering equal an
  `slog.Info` call with the same attributes (tested). The ring tee drops the line because
  `permission_reply` is not in the dashboard `auditEvents` allowlist; changing that is a dashboard
  change and is not part of this slice (proposal below).
- **What can be observed.** With Go's default handler and the `log` writer, a writer error
  (`EPIPE`, closed file, `io.ErrShortWrite`, any error) reaches the caller of `Handle`; the tests
  cover a failing writer, a short write that returns an error, a closed file and a broken pipe.
  **Not observable:** a writer that reports a short write with a nil error (it breaks the
  `io.Writer` contract; `os.File` does not). Journald/stderr delivery after a successful `write(2)`
  is not observable either; "written" means "the destination accepted the bytes".
- **Latch (4.2 step 9).** After one failed write (handler error, panic, or a handler that does not
  accept Info records) `Writer.Unavailable()` is true for the life of the process and every later
  `Write` returns `ErrUnavailable` without calling the handler. It never resets on a later
  success (there is no later write). Fields refused before writing (`ErrInvalid`) neither write nor
  latch.
- **Fields.** Every attribute has a closed shape (regex); an unknown key, a value outside its
  shape, a missing required field (`tool`, `attempt_id`, `decision`, `permission_id`,
  `execution_id`) is refused. `event`, `phase`, `decided_by` and `human_verified` cannot be
  supplied.
- **Refusal value.** Originally `audit.Refusal()`; as built in S3 it is
  `execution.AuditUnavailable()` (`audit` no longer imports `execution`): `UPSTREAM_FAILURE`,
  `retryable: true`, `reason=audit_unavailable` (the reason code of 4.2 step 8).
- **Changes in S3 (reviews #277 C / #278 B on #275).** P3-1: a fan-out handler (the dashboard tee)
  that reports Enabled for its ring while its journal branch refuses the record is no longer reported
  as a written line; the tee exposes `JournalEnabled` and `Write` requires it (an Info journal still
  works, a Warn journal latches). P3-4: `NewWriter` is unexported; production has the one process-wide
  latch `Default()`, and other packages fake the `AttemptWriter` interface of their consumer. P3-3:
  cross-field checks, now that a call site exists: `attempt` is required, `attempt=2` iff `supersedes`
  is present and never equals `attempt_id`, `allow_once` is never recorded under
  `policy_mode=reject_only`, and `native_tool`, `kind`, `scope_digest`, `pattern_count` are all present
  or all absent. P3-2 (a hung journal is not bounded by the context) is recorded in 4.2 step 8.

Proposals for the maintainer (not implemented): (1) recovery of the latch: restart only (as built),
or a probe write after a back-off, or an operator command; (2) whether `reject` and same-key
replays that send nothing are still served while latched (the writer cannot tell; the S5 call site
decides); (3) add `permission_reply` and its attributes to the dashboard `auditEvents`/`auditAttrs`
allowlists so the audit ring shows attempts; (4) whether `audit_unavailable` needs its own error
code instead of `UPSTREAM_FAILURE`.

## As built (S3), 2026-11-15

Facts only (task #282). `reply_permission` with decision `reject` exists against a fake native; it
is **not registered in the production server** and a deployed Bridge keeps its eleven tools.

- **Gating.** The tool is registered only when `Server.PermissionRelay` is set (mode `reject_only`);
  `cmd/bridge` never sets it (D15 Q3: registration lands with S5, when its audit lines exist). The
  manager also has no production native reply client (`PermissionReplier`) and no attempt writer
  installed, so a call would fail closed (`UPSTREAM_FAILURE`) even if registered. A direct call while
  unregistered returns `INVALID_INPUT`. Policy mode (S2/S4) does not exist: any mode other than
  `reject_only` leaves the tool absent.
- **Where the logic is.** `internal/mcp/replypermission.go` (steps 2 and 3: `allow_once` refused with
  zero native calls before the execution lookup, Claude executions refused, ACL and workspace check);
  `internal/execution/permissionreply.go` (step 4 record rules, the run token, step 5 fresh re-read,
  step 7 identity re-check, step 8 attempt record then the one native write). `reply_permission` is in
  `mutating()` (read-only refusal reads nothing natively).
- **Order, as built.** Read-only; `allow_once`; Claude execution; execution exists; ACL read; the
  execution waits with the guard held by itself; same-key and `supersedes` rules (in memory); take
  the reconcile slot and the run token and **re-check both** (a concurrent call may have settled the
  record); fresh `PermissionView`; `revalidateSession`; record `in_flight`; attempt line; native
  write. The re-read, the identity check and the write happen under the run token, so an interrupt, a
  prompt, a probe or a release cannot interleave.
- **Closed outcomes.** Native 404: `NOT_FOUND`/`resolved_elsewhere_or_dropped`, record `refused`.
  Native 400: `UPSTREAM_FAILURE`/`native_rejected_id`, record `refused`. Every other failure (timeout,
  lost response, 5xx, canceled context) is `UNKNOWN_OUTCOME` with `attempt_id` and `attempt`, record
  `unknown`; it is never resent. These two mappings are author choices (the ADR names only 400 and the
  lost response): a 401/403 is treated as unknown (conservative; costs at most one explicit supersede).
- **Differences from the S3 test row, stated.** (1) The `phase=result` write failure after the native
  call and "every later call refused with `audit_unavailable`" need the result line, which is S5; the
  attempt writer today writes only `phase=attempt`. What S3 pins is the attempt-record failure (zero
  native writes, `audit_unavailable`, record `refused`) and that the process-wide latch refuses
  everything after any failed write. (2) `IDEMPOTENCY_CONFLICT` (opposite decision on an answered
  request) and "supersede with a different decision" are unreachable through the tool in `reject_only`
  (`allow_once` is refused first); they are implemented in the manager and tested there with a seeded
  record. (3) The generic AUDIT-01 middleware writes **no** line for `reply_permission`: adding the
  tool to `mutating()` would otherwise make it write a prompt-shaped line; the attempt line is the only
  line until S5.
- **Failed supersede.** A failed attempt-2 attempt record restores the earlier `unknown` attempt, so a
  failed supersede cannot turn the next same-key call into a fresh first attempt.
- **Not done (maintainer proposals unchanged).** Latch recovery (restart only), the dashboard ring
  allowlist, a distinct `audit_unavailable` error code.
- **S1 follow-ups fixed here (#208/#210 P3s).** `get_session` reports a measured zero
  `pending_permission_count` while waiting; a `resolved_externally` tombstone no longer hides the one
  pending request (an uncertain entry still does); the `PermissionSource` call has its own deadline.
  The waiting-state condition of `describe` is pinned by a test (a running guarded execution with a
  measured zero reports no count).

## As built (S5), 2026-11-15

Facts only (task #288). `reply_permission` (reject) is registered by default and writes its audit
lines; the production replier exists; the S3 review hardening is in.

- **Registration.** `cmd/bridge` calls `wirePermissionRelay` with the mode of `BRIDGE_PERMISSION_RELAY`
  (unset or `reject_only`: the OpenCode client as the one `PermissionReplier`, `audit.Default()` as the
  `AttemptWriter`, `Server.PermissionRelay = reject_only`; `off`: nothing installed, eleven tools; `policy`
  and any other value: startup error, policy mode is not started, D15 Q1). The variable and its values are
  ADR section 5's; that startup refuses `policy` instead of treating it as `reject_only` is an author choice.
- **No production `PermissionSource`.** Nothing installs one: the observer needs an authority
  (`PermissionObservationAuthority`: scope with principal, native version, policy revision; the manager's
  submitted executions) that does not exist. So on a deployed Bridge the re-read of 4.2 step 5 fails closed
  (`UPSTREAM_FAILURE`, before any attempt line and any native write) and `pending_permission` is count-only.
  The registration is in place as D15 Q3 decided; the tool cannot answer a request until the source slice.
- **Replier.** `opencode.Client.ReplyPermission(ctx, session, directory, requestID, "reject")`: one `POST
  /permission/:requestID/reply`, body `{"reply":"reject"}`, `?directory=`; only `reject`, only well-formed
  targets. The manager passes the run's own directory (the workspace-bound session routing is the one of
  prompt and abort: directory only).
- **Lines.** `internal/audit` writes the three phases (`attempt`, `result`, `refused`) through one writer, one
  lock, one latch; the manager writes `attempt` and `result` (it holds the details), the MCP audit hook writes
  the one line of every other return path through `Manager.AuditReply`, using a per-call `ReplyTrace` so a call
  that the manager closed is not closed twice. `result` carries `outcome` replied / not_found /
  native_rejected_id / unknown_outcome (or replay), `accepted`, `error_code`, `reason`. Closed-shape values only
  (`audit.Clean` drops anything else; an unknown error code becomes `INTERNAL`). `decided_by=chat_client` and
  `human_verified=false` in every line and in the result. The generic AUDIT-01 writer writes nothing for this tool.
- **Step 9, decided.** After any failed audit write every later call is refused with `audit_unavailable`,
  including a replay that would send nothing (answer to the open #197 item "whether same-key replays are still
  served"): a call whose line cannot be written is not served. The MCP layer asks first, so a latched call makes
  no native call. A failed result line leaves the outcome and record intact and latches. Recovery stays restart
  only. P3-2 decided in 4.2 step 8 (bound by the context).
- **Hardening (reviews #285 B, #286 C on #283).** Panicking replier: record `unknown`, `UNKNOWN_OUTCOME` with
  `attempt_id`, result line, supersede works. One id bound end to end (`permission_id` 128, session id 256).
  Slot/token wait deadline: retryable `UPSTREAM_TIMEOUT`. Manager-side `allow_once` refusal before any native read.
  Directory identity re-check pinned (replaced workspace `FORBIDDEN`; filesystem timeout retryable; no write, no
  attempt line). 401/403/429/5xx pinned as `UNKNOWN_OUTCOME`.
- **Author choices.** A replay of a recorded success writes a `result` line with `outcome=replay`; a replay of a
  recorded `UNKNOWN_OUTCOME` writes a `refused` line quoting the recorded attempt; a refused line may quote
  `attempt`/`attempt_id` only for that replay. Not done (maintainer proposals, unchanged): latch recovery, the
  dashboard ring allowlist for `permission_reply`, a distinct error code for `audit_unavailable`.

**Delta (task #295, 2026-11-15).** `main` (#291) was merged into the S5 branch as a plain merge
commit; `newMCPServer` constructs the server and the `BRIDGE_PERMISSION_RELAY` wiring applies to it
(tool counts: eleven with `off`, twelve by default, thirteen with `create_session`). Reviews #292 P3-1
and #293 P3-3: the result and refused/replay lines are pinned to `WithoutCancel` plus their own 5 s
bound. Review #293 P3-2: the attempt line's `kind` shape now equals the Bridge's accepted shape
(`[A-Za-z0-9_.:-]{1,64}`); the other option (a typed refusal before the attempt) was not needed because
the charset is the same closed one `native_tool` already uses.

## As built (S6), 2026-11-16

Facts only (task #304). The production `PermissionSource` exists and is wired; the verified path is
tested against a fake native; no live run was made.

- **Source.** `internal/execution/permissionsource.go`: `NewOpenCodePermissionSource(client, manager,
  cfg)`; every `PermissionView` builds a fresh `opencode.PermissionObserver` for the session and takes
  one bounded `Snapshot`. No cache (a cached view would let a reply be checked against a request the
  user has already answered). `cmd/bridge` installs it with the replier and the attempt writer in
  `reject_only` (mode `off` installs nothing; `wirePermissionRelay` now returns an error).
- **Authority.** The observer's `PermissionObservationAuthority` is a read-only projection of the
  manager's runs: the scope comes from the run holding the session guard (none: no scope, nothing
  observed), the executions from the manager's submitted runs of the session with their
  Bridge-chosen user message IDs. Neither touches a native payload. Locks: the manager's mutex and a
  run's mutex are never held together; the authority is called only from the reconcile path and from
  step 5 of the reply, where no run mutex is held.
- **The gap that remains (decided here as fail-closed, not as a decision).** The observer needs native
  incarnation evidence (`PermissionScope.NativeEpoch`) before it will report `attribution=verified`;
  ADR-018 and `docs/P2_PERM_CORE_02_OBSERVATION.md` say none exists and none may be fabricated. The
  source has an optional `NativeIncarnation` seam and production passes none, so on a deployed Bridge
  every request is `unattributed`: `pending_permission` stays count-only and `reply_permission`
  refuses with `FORBIDDEN`/`not_verified`, with no native write and no attempt line (S5 failed closed
  with `UPSTREAM_FAILURE` because no source existed at all; now the source runs and the refusal says
  why). What can supply the evidence
  is a maintainer decision (see the BACKLOG item); the options and their trade-offs are listed there.
- **Tests (fakes only).** A fake native server (GET `/permission`, `/session/:id`,
  `/session/:id/message/:id`, POST `/permission/:id/reply`) behind the real client, observer and
  manager: the verified request is exposed with the closed projection; a reply makes a fresh read, one
  attempt line and exactly one `{"reply":"reject"}` POST; the binding refusals (absent, another
  session, tool-less, an assistant message of another workspace, a user message the Bridge never
  sent, a request attributed to another execution of the same session) each make no native write and
  no attempt line; a request answered natively between a view and the reply is refused; a transient
  native failure refuses and the next call works; a hanging read is a retryable `UPSTREAM_TIMEOUT`
  inside its bound; a lost response is `UNKNOWN_OUTCOME` and never resent, one explicit superseding
  reject is a second write and a third is refused (D15 Q8); `allow_once` is refused with zero native
  calls. Mutations: reply without the fresh read (9 RED), a cached view (6 RED), accepting a
  permission of another execution (2 and 1 RED), `allow_once` not refused (2 RED), a non-reject
  reply forwarded by the client (1 RED), a fabricated epoch (1 RED), the wiring dropped (1 RED), mode
  `off` installing the source (2 RED).

## Amendment A1: Incarnation source (PROPOSED, task #315, 2026-11-16)

**Status: PROPOSED.** Design and research only: no code, no test, no live run. Acceptance is the
maintainer's (D19); nothing here changes D12, D15 (reject-only, policy OFF), an accepted section of this
ADR, ADR-018 or ADR-010. Upstream facts are static source reading of OpenCode v1.19.4 `148842ef` (a
fresh sparse checkout of `packages/opencode/src` and `packages/schema/src`, read only; OpenCode was not
run and no Bridge or OpenCode call was made). Paths below are under `packages/opencode/src/` and
anything not settled by that reading is marked **UNVERIFIED**. Bridge-side citations were re-checked
against `main` `b98c004` (after #306 and #307), where the section "As built (S6)" above describes the
production source; revision 2 (task #328) is the review-fix pass of #324 and revision 3 (task #333) the pass of #330.

### A1.1 The finding and what "incarnation" has to mean

The production `PermissionSource` (S6, PR #306, merged; see "As built (S6)") has no incarnation source:
`PermissionSourceConfig.Incarnation` is nil in production (`internal/execution/permissionsource.go:48`),
so `runAuthority.PermissionScope` supplies an empty `NativeEpoch` (`:121-134`), the check in `correlate`
(`permission_observation.go:290-291`, function at `:281`) answers `unattributed/identity_unverified` for
every request, `pending_permission` is count-only and `reply_permission` refuses
`FORBIDDEN`/`not_verified`. The seam is `NativeIncarnation.NativeEpoch(ctx) (string, error)`
(`permissionsource.go:33-40`). `docs/P2_PERM_CORE_02_OBSERVATION.md` and ADR-018
section 5 forbid fabricating the value.

The pending requests do **not** live for the lifetime of the OS process. They live in the per-directory
instance state:

- `permission/index.ts:46-63`: the pending map is created by `InstanceState.make`; its finalizer
  (`:54-60`) fails every pending request with `RejectedError` and clears the map.
- That instance state is disposed or reloaded **without a process restart** by `POST /instance/dispose`
  (`groups/instance.ts:44,62-67`, torn down by `lifecycle.ts:23-55`), `POST /global/dispose`
  and `PATCH /global/config` when the config changed (`handlers/global.ts:78-86`), and by an instance
  reload (`project/instance-store.ts:126-144`); each emits `server.instance.disposed` per instance
  (`emitDisposed`, `project/instance-store.ts:79-97`), and the global paths then also emit the separate
  **`global.disposed`** (`server/global-lifecycle.ts:6-14`; A1.2 row 4).
- A reply addresses a request by ID and answers `NotFoundError` when the ID is not pending
  (`permission/index.ts:109-113`). Request IDs are an ascending time-and-counter prefix plus 14 random
  base62 characters, about 83 random bits (a permission request ID is `PermissionV1.ID.ascending()`,
  `permission/index.ts:86`, defined at `packages/schema/src/v1/permission.ts:10-12` as `"per_" + ascending()`
  with `ascending` from `packages/schema/src/identifier.ts:6-30`; the opencode package's own prefixed
  generator `id/id.ts:22-24,51-69` uses the same scheme), so **a stale ID does not collide with a new
  one**: the incarnation problem is not "a reply hits another request". This depends on callers supplying
  no ID of their own: `AskInput` has an optional `id` (`packages/schema/src/v1/permission.ts:51`) and
  `ascending(id?)` returns a given ID unchanged (`:12`); the one in-tree caller that supplies one
  (`session/llm.ts:164,187-188`) generates it with the same `PermissionV1.ID.ascending()`, so the conclusion
  holds today.

So the property attribution needs is: *the request belongs to a turn that started in the same instance
incarnation the Bridge submitted its prompt into.* The residual threat (T-I) is attribution, not ID
reuse: after a restart or a dispose the user message of an old execution is still stored natively; if any
actor later starts a turn whose assistant message has that user message as `parentID` (a retry or
regenerate-style action; which native actions do this is **UNVERIFIED**), `correlate` would attribute its
new request to the old Bridge execution. With reject-only the consequence is that Chat rejects a request
the human wanted to answer (integrity and availability of that decision, no escalation; and the native
`reject` also rejects the other pending requests of the session, `observation` doc). That is what the
epoch must exclude.

### A1.2 Research: native identifiers at `148842ef`

| # | Candidate (file:line) | Stable across restarts? | Changes on restart? | Spoofable by a client? | Verdict |
|---|---|---|---|---|---|
| 1 | `GET /global/health` `{healthy, version}` (`routes/instance/httpapi/groups/global.ts:12-15,79-87`, `handlers/global.ts:66-68`) | yes (the version) | no | a client cannot change the answer, but it is not unique | **no identity** |
| 2 | SSE `server.connected` and `server.heartbeat` (`handlers/event.ts:63-71`, `handlers/global.ts:25-52`) | each event has a fresh `EventV2.ID.create()` and empty properties | a new ID per event, not per boot: `EventV2.ID` is `Event.ID` (`packages/core/src/event.ts:14`), defined in `packages/schema/src/event.ts:9-11` as `"evt_" + ascending()`, i.e. a 6-byte time-and-counter prefix plus 14 random base62 characters (`packages/schema/src/identifier.ts:6-30`) | no | **no identity** (source-verified) |
| 3 | `GET /path` `{home, state, config, worktree, directory}` (`handlers/instance.ts:29-36`) | yes | no | no | **no identity** |
| 4 | Two distinct event types. `server.instance.disposed` with `properties.directory` (the literal at `server/event.ts:8`, emitted per instance by `emitDisposed`, `project/instance-store.ts:79-97`, from `disposeContext` `:94-97` and the reload `:138`). And **`global.disposed`**, a different type: `Disposed = Event.define({ type: "global.disposed", schema: {} })` (`packages/schema/src/server-event.ts:6`), reached as `Event.Disposed` through `ServerEvent` (`server/event.ts:4`) and emitted with envelope `directory: "global"` and empty properties (`server/global-lifecycle.ts:6-14`). `disposeAllInstancesAndEmitGlobalDisposed` runs `store.disposeAll()` first, which disposes each cached instance (one `server.instance.disposed` each, `instance-store.ts:166-185`), and then emits `global.disposed` (`global-lifecycle.ts:16-26`) | not an identifier: an **event** | emitted on dispose, reload, config change and graceful shutdown; **not** on a crash, kill or power loss | a client can trigger a dispose (more refusals only); an emitted event cannot be hidden from an open stream | **usable as a signal**, not as a value |
| 5 | `OPENCODE_PID` (`index.ts:77`) | a value of the process | yes | n/a | **not exposed**: no route reads it (searched `packages/`); in-process environment only |
| 6 | Permission and event IDs (permission: `permission/index.ts:86` and `packages/schema/src/v1/permission.ts:10-12`; event: `packages/schema/src/event.ts:9-11`; both generated by `packages/schema/src/identifier.ts:6-30`; same scheme as `id/id.ts:22-24,51-69`) | no (time prefix plus random) | new IDs | the values come from the native process clock | **forbidden as evidence** (native-writable), and not an identifier of the process |
| 7 | Listener host and port, mDNS (`server/server.ts`) | yes (configuration) | no | n/a | **no identity** |

**Result: no native instance or boot identifier is exposed over HTTP at v1.19.4** (source reading only;
the `/doc` OpenAPI document of a running server was not read, and other versions are not covered).
Option (a) below therefore does not exist today.

### A1.3 Options

For each: security against a stale or reused permission ID, races, fail-closed behaviour, what a
reviewer can test.

**(a) A native identifier.** None exists (A1.2). The slot stays open: if a later OpenCode exposes one,
it plugs into the same `NativeIncarnation` seam. Not available.

**(b) A host-local process incarnation.** The value is the kernel's: `starttime` (field 22 of
`/proc/<pid>/stat`) plus `/proc/sys/kernel/random/boot_id`, with the PID of the OpenCode server taken from
its configured unit (`MainPID`) or a configured PID source.
- *Trust:* not writable by the native server's model, by a Chat client or by an API client; independent.
- *Stale or reused ID:* covered for a **process** restart (the value changes); **not** covered for an
  instance dispose or reload, which replace the pending store without changing the process (A1.1): a
  client with API access can do that with `POST /instance/dispose`. So (b) alone does not match the
  lifetime of the pending store.
- *Failure modes:* the Bridge in a container or another host (no `/proc` of the server: refuse); a
  PID-reuse race (the start time and boot ID make it detectable); a restart between two reads (read before
  and after the observation and compare); PID discovery needs either a systemd property read (a new
  dependency on a unit name that the Bridge does not have today) or `/proc/<pid>/fd` access (same user or
  root); a permission error means no value, so fail closed. A remote OpenCode is unsupported.
- *Tests:* parse `/proc` fixtures through an injected file system; mutation tests for the compare.

**(c) A Bridge-observed epoch.** One long-lived subscription of the Bridge to `GET /global/event` (one
connection for all directories; `handlers/global.ts:25-52`). The Bridge keeps an `epoch` value that **it
generates itself** (a random 128-bit token, never derived from an event ID, message text or any native
value) and replaces it whenever continuity of observation may have been lost:
  1. on every (re)connection of the stream, including the first;
  2. on **any** `server.instance.disposed` (whatever its `directory`) **and** on `global.disposed`: they
     are two different event types (A1.2 row 4), so a matcher for only one of them, or for the run's own
     directory only, would miss a replacement. `global.disposed` is emitted after the per-instance events
     of `disposeAll` (`global-lifecycle.ts:19-23`), so instances that held pending requests are already
     covered by their own `server.instance.disposed`; the second rule is the fail-closed superset (it also
     covers the case where `disposeAll` fails and the swallow option still emits `global.disposed`,
     `global-lifecycle.ts:20-23`);
  3. on stream end or error, and when no event (heartbeats arrive every 10 s,
     `handlers/global.ts:35-37`) has been seen for 25 s (a gap rule; the value is a proposal).
The epoch is **armed** only after the first heartbeat of a connection. The heartbeat is
`Stream.tick("10 seconds")` with `Stream.drop(1)` (`handlers/global.ts:35-37`), so the expected arming
delay after every (re)connection, including Bridge start, is **at least one heartbeat interval (about 10
s)**; the exact first-heartbeat time is not observed (UNVERIFIED). During that delay every new run is
unverifiable (it records an unarmed `submitEpoch`, below), and so is any run already waiting; the
operator sees `not_verified`/count-only, never a wrong attribution. Every run records the epoch that
was armed when its prompt was submitted (`run.submitEpoch`, Bridge memory only, ADR-010). The authority
supplies `NativeEpoch` = the current epoch **only if** it is armed and equal to the run's `submitEpoch`;
otherwise the epoch stays empty and the request is unattributed. `PermissionView` reads the epoch before
and after its `Snapshot` and refuses attribution when they differ.
- *Stale or reused ID and T-I:* an execution submitted before any process restart, dispose, reload,
  config change, upgrade or stream interruption **can never be verified afterwards** (its epoch is gone):
  fail closed, the human answers natively. A new turn in a new incarnation cannot be attributed to an old
  execution.
- *Races and limits (stated, not hidden):* (i) event latency: a dispose happens natively before its event
  reaches the Bridge, so for a few milliseconds an observation of the new instance can still carry the old
  epoch (reject-only, and the full binding of ADR-018 section 5 still has to hold; residual). (ii) Lost
  events at connect: the hello is written before the `GlobalBus` listener is registered. By construction
  of the response, `Stream.make(hello)` is concatenated with `events` (`handlers/global.ts:41-42`), and
  `GlobalBus.on` runs inside `Stream.callback`'s acquire step (`:28-33`), which only starts when `events`
  is consumed after the hello; the per-instance `/event` handler instead registers eagerly
  (`handlers/event.ts:29-33`). Hence arming after the first heartbeat and the rule that older executions
  are unverifiable. This is source reading of the stream construction; its runtime behaviour behind a
  running server or a buffering proxy was not observed (**UNVERIFIED**). (iii) A
  Bridge restart resets everything (executions are memory-only anyway). (iv) A silent middlebox drop is
  caught only by the 25 s gap rule, so for up to about 25 s the old epoch stays armed while the native
  instance may already have been replaced (the same effect as (i), but seconds long, not milliseconds). (v) A compromised native server could hide events: out of the boundary,
  as in ADR-020. (vi) It costs one authenticated long-lived connection.
- *What it is:* **evidence of continuity of observation**, not a native identity. It meets the purpose of
  the epoch (T-I), but not the literal wording "independent native incarnation evidence"; whether that is
  acceptable is question Q1. **If Q1 is accepted**, the as-built wording must be reworded to "continuity
  of observation" in two places, which this amendment deliberately does **not** change now: the doc
  comment of `NativeIncarnation` (`permissionsource.go:33-37`, "changes when the native process that
  holds the pending requests is replaced") and `docs/P2_PERM_CORE_02_OBSERVATION.md` (about `:60-68`,
  "native incarnation" and "process incarnation"). It would also need ADR-018 section 5 to be read the same way.
- *Fail closed:* no stream, 401, parse failure, not yet armed, a gap: empty epoch, `not_verified`.
- *Tests:* a fake SSE server (A1.5).

**(d) Stay `not_verified` (the relay is inert).** No new risk, no new state; `reply_permission` stays
registered but always refuses and `pending_permission` stays count-only. The operator can set
`BRIDGE_PERMISSION_RELAY=off` to stop listing a tool that cannot act. Cost: the feature never works.

**(e) Complementary, not an incarnation: pin the assistant messages.** The Bridge already reads the
assistant messages correlated with its execution during reconciliation; recording their IDs and requiring
a request's `tool.messageID` to be one of them excludes the retry-style turn of T-I without any epoch
(a new turn has a new assistant message). It can narrow (c)'s residuals (i) and (iv), but only if the pin
is made by a read that **precedes** the read that first lists the request (the pin rule below), and it
depends on the Bridge having observed the message before the request, so it cannot replace (c).

*Pin rule (a proposal; revision 3, #330 P2-2).* **Chosen: option (a).** A request whose assistant message
is not pinned **before the request was first listed** is refused, not attributed on the epoch alone, and
not pinned and verified in the same view. Precisely:
- **Who pins.** The pinned set is per execution and Bridge memory only (ADR-010). It is written only by the
  execution's own reconciliation path (the path that already reads the run's session messages), never by
  `PermissionView`. A message ID is pinned only by a reconciliation read made while the run holds the
  session guard and the epoch is armed and equal to the run's `submitEpoch`.
- **When it counts.** A pin is usable for a request only if its read completed **strictly before** the
  permission-list read of the first view that listed that request (the view records a per-request
  "first listed" mark in memory, or equivalently takes a snapshot of the pinned set before it lists
  permissions). A pin made after the request first appeared never verifies that request.
- **Refusal.** A request whose `tool.messageID` is not in the usable pinned set of the execution it
  correlates to is `unattributed` (proposed reason `message_unpinned`): `pending_permission` stays
  count-only, `reply_permission` answers `FORBIDDEN`/`not_verified`, zero native writes, no attempt
  line, reject-only (D15, Q8 unaffected). The human answers natively.
- **Why (a), not (b).** Under the earlier text a message could be pinned and verified in one view under
  the same armed-and-equal epoch as (c), so (e) added nothing in windows (i) and (iv) (#330 P2-2). Rule
  (a) makes the pin a separate, earlier observation: a retry-style turn (T-I) whose new assistant message
  and request first appear inside a stale-epoch window cannot be verified, because no earlier read pinned
  its message. Option (b) (a cost-only refusal rule that claims no narrowing) was the alternative; (a) was
  preferred because it keeps a real, testable protection and can only refuse more (it never relaxes a
  condition of (c) or of ADR-018 section 5).
- **Cost, stated.** A request whose message the reconciliation path had not yet read when the request
  appeared is never verified, even later: for example the first permission of a fresh assistant message.
  The relay then answers `not_verified` for such requests and works only for requests of messages the
  Bridge read earlier (for example a later tool call of an already-pinned message). This can make the
  relay refuse most first-asks; that is fail-closed and inside D15, and it is the main input for Q6.
- **Residual, stated.** (e) narrows (c) but does not remove (i) or (iv): a message created in a stale-epoch
  window and pinned by a reconciliation read inside that same window **before the request appears**
  (milliseconds for (i), up to about 25 s for (iv), for instance when a long tool call precedes the
  permission) is still pinned under the old epoch and the later request is attributed. Pinned IDs are
  Bridge-observed values, not trusted native assertions.

### A1.4 Recommendation

**Adopt (c), with (e) (option (a), with its stated cost) as an optional complement in the same slice (Q6), and keep (b) as a later optional
hardening for process restarts that does not rely on the stream.** Reasons: (c) is the only candidate
that follows the lifetime of the pending store (instance disposal included), needs no host privilege,
works for a remote OpenCode, adds no authority (the epoch is a Bridge-generated token that can only be
replaced, never forged into a verification) and fails closed everywhere. It stays inside D15: replies
remain reject-only, policy mode stays off, `allow_once` stays refused with zero native calls, and every
check of ADR-024 4.2 and ADR-018 section 5 still has to pass; the epoch only **adds** a condition to
verification (a continuity bound) and lifts the blanket `unattributed` that makes the relay inert.
If the maintainer does not accept (c)'s weaker meaning (Q1), the remaining choices are (b) with its
privilege and deployment requirements, or (d).

### A1.5 Implementation slice S7 (proposal) and tests

Files (proposal): `internal/adapters/opencode/epoch.go` (the tracker: one SSE reader of `/global/event`
with the existing size bounds, deterministic reconnect backoff 1 s doubling to 30 s, `Epoch() (token,
armed bool)`, `Stop`; its adapter to the existing seam returns `("", nil)` while unarmed),
`internal/execution` (`run.submitEpoch` captured at admission through the same seam, `""` meaning
unarmed and therefore unverifiable; `runAuthority.PermissionScope` (`permissionsource.go:106-139`) sets
`NativeEpoch` only when armed and equal; `PermissionView` compares the epoch before and after the
`Snapshot`; the **`NativeIncarnation` seam is unchanged**, `NativeEpoch(ctx) (string, error)`, since the
armed state is the tracker's own `Epoch()` result and an empty string already means unarmed), and
`cmd/bridge` (start and stop the tracker for `reject_only`; the default is **off** behind a proposed
`BRIDGE_PERMISSION_INCARNATION=observed`, so the first slice changes no deployed behaviour). If (e) is
included (Q6), the per-execution pinned set is written by the execution's reconciliation path and read
through the authority: a new field on `opencode.KnownPermissionExecution`
(`permission_observation_types.go:37-41`, returned by `PermissionExecutions`, `permissionsource.go:144`),
for example the pinned assistant message IDs usable for that execution, and `correlate`
(`permission_observation.go:281`) gains **one additional refusal condition** (`message_unpinned`) beside
the existing ones. No existing attribution condition is changed or relaxed, and there is no change to
`allow_once` handling or to the client's reject-only reply. Without (e) the observer is unchanged.

Tests (fake SSE and fake native, `-race`, `-count=20`, goroutine-leak check after `Stop`): not armed
before the first heartbeat; armed after it; a dispose event, a global disposed event, a stream end, a
reconnect and a 25 s heartbeat gap (injected clock) each replace the epoch; a 401 or malformed stream
never arms; the epoch value is unrelated to every native value (two servers with identical event IDs give
different tokens); an execution submitted before a replacement is unverifiable (`not_verified`, zero
native writes, no attempt line); one submitted while not armed is unverifiable; an epoch that changes
between the two reads of one `PermissionView` refuses attribution; oversized or malformed events are
bounded and ignored; a request attributed after a replacement is refused; `allow_once` stays refused with
zero native calls; `BRIDGE_PERMISSION_RELAY=off` starts no tracker. A
`server.instance.disposed` for any directory (including one other than the run's) replaces the epoch,
and so does `global.disposed` (envelope `directory: "global"`) arriving **alone**, and a `global.disposed`
after a failed `disposeAll`. The (e) pin rule (option (a)): a request whose assistant message is not
pinned before the request was first listed is `unattributed` (`message_unpinned`), `reply_permission`
answers `FORBIDDEN`/`not_verified` with zero native writes and no attempt line, and stays refused even
after the reconciliation path pins that message later (a request seen first is not verified by a later pin);
a request whose message was pinned by an earlier reconciliation read verifies; a pin written by
`PermissionView` itself is impossible (the view has no write access); a message pinned for another
execution, or pinned while the epoch was unarmed or different, never verifies; an unarmed tracker
returns `("", nil)` and not an error (an error from the seam fails the scope read,
`permissionsource.go:123-125`, instead of yielding an unattributed request).

Mutations (each alone must go RED): arm at the hello; ignore the dispose event; ignore the global disposed
event; ignore stream end; drop the heartbeat-gap rule; derive the epoch from an event ID; read the epoch
once instead of before and after; skip the `submitEpoch` equality; supply an epoch when the tracker is
nil; keep the epoch across a reconnect; let `Stop` leave the goroutine; start the tracker in mode `off`; match
only `server.instance.disposed` (a `global.disposed` goes unnoticed); only `global.disposed`; match the
disposed event on the run's directory only; attribute an unpinned message on the epoch alone; let
`PermissionView` pin the message it is about to verify (pin and verify in one view); accept a pin made
after the request was first listed; pin a message without the armed-and-equal epoch; keep pins across an
epoch replacement.

### A1.6 Live acceptance outline (R5b style, reject-only)

Operator present, disposable root, a session the Bridge may use, no provider or harness configuration
read, evidence captured as in the runbook; anything not run is **NOT_EXERCISED**, never PASS. Steps marked
M need the maintainer.
1. Start the Bridge with `BRIDGE_PERMISSION_RELAY` unset and the incarnation mode on (M). The startup log
   states that the tracker is armed; wait at least one heartbeat interval (about 10 s, UNVERIFIED) before
   step 2, because a run submitted before arming is unverifiable by design.
2. Send a prompt that makes the native session ask for a permission; `get_execution_status` shows
   `waiting_for_permission` and a `pending_permission` object with `attribution: "verified"` (no pattern).
3. `reply_permission(reject)`: exactly one native `POST /permission/:id/reply` with `{"reply":"reject"}`,
   one `attempt` and one `result` journal line; the execution is reported through ADR-018's
   `permission_rejection`; the guard is not released by the Bridge.
4. Negative (M): with a request pending, dispose the instance natively (`POST /instance/dispose`, an
   operator action); start a new turn; the new request is **not** verified for the old execution:
   `pending_permission` absent, `reply_permission` answers `FORBIDDEN`/`not_verified`, no native write.
5. Negative (M): restart the OpenCode server; the old execution is unverifiable, the count only is shown.
6. Negative: block the event stream (stop the tracker or the route); within 25 s requests become
   unverified; the tool refuses.
7. `reply_permission(allow_once)` answers `FORBIDDEN`/`allow_once_disabled` and the native request log
   shows no call.
8. Capture: native request log, Bridge journal, tool results, the tracker's log lines; no secret.

### A1.7 Open questions for the maintainer (D19)

- **Q1.** Is a Bridge-observed continuity epoch (c) an acceptable meaning of "native incarnation" for
  ADR-018 section 5, given that it proves continuity of observation and not a native identity?
- **Q2.** Default of the new mode: off in the first slice (recommended) and a later decision to flip it
  after the live acceptance, or on from the start?
- **Q3.** Also require (b) when the server is host-local, as a second independent signal, or keep it as a
  later hardening?
- **Q4.** One global subscription (recommended) or one `/event` per active directory?
- **Q5.** The heartbeat-gap threshold (25 s proposed, 2.5 heartbeats) and the backoff bounds.
- **Q6.** Include the assistant-message pin (e) in S7, or defer it? The proposed rule is option (a) of
  A1.3(e): pinned before the request was first listed, otherwise refused (never attributed on the epoch
  alone, never pinned and verified in one view). It narrows windows (i) and (iv) but makes the relay
  refuse requests whose message was not read earlier (possibly most first-asks); without (e) the relay
  verifies more, with the full residual of (i) and (iv). The maintainer decides.
- **Q7.** Until S7 is accepted and exercised, should the operator be told to run with
  `BRIDGE_PERMISSION_RELAY=off` (the runbook currently says the tool is registered by default)?

### A1.8 Limitations of this amendment

Source reading of one upstream commit; no OpenCode, Bridge or host service was contacted. The
`EventV2.ID` scheme and the hello-before-listener ordering are settled by source reading (A1.2 row 2,
A1.3(c)(ii)); their runtime behaviour is not. **UNVERIFIED:** the time of the first heartbeat (expected at
least one 10 s interval, not observed), which native actions create an assistant message whose `parentID`
is an existing user message (T-I is a hypothesis about such an action; upstream `session/prompt.ts`
creates assistant messages with `parentID: lastUser.id` inside its run loop, but which public endpoint
starts that loop without a new prompt was not determined), the behaviour behind proxies that buffer SSE,
other OpenCode versions, and the cost of one extra long-lived connection. The recommendation is a
proposal; every name, threshold and default above is a proposal.


## 13. Limitations of this record

- Static reading of one upstream commit; OpenCode was not run and no Bridge or
  OpenCode call was made. Line numbers for `perm:115-120` and `perm:121-166`
  are carried from the P4-PERM research and were not re-traced here
  (UNVERIFIED beyond that); `perm:109-114`, `hnd-s:362-378`, `grp-s:395-408`
  and `schema/v1/permission.ts:27-38` were re-read for this ADR.
- Claude's behavior for tool annotations and per-call approval, and whether
  Claude Chat elicits at all, are UNVERIFIED (P4-PERM).
- Defaults, tool names, closed reason codes (including the choice of
  `UPSTREAM_FAILURE` for `audit_unavailable`) and config keys are proposals.
- The attempt-writer requirement (4.2 step 8) is a **new** requirement on the
  implementation and depends on the open ROADMAP item for Mutation Gate
  integration; this ADR does not design that writer.
- The `PermissionObserver` re-read costs more native reads than a bare
  `GET /permission` and runs inside the observer's 5 s bound; I did not measure
  it.
- The original record: no code, test or live run was done for this ADR; the as-built notes above are
  the implemented and tested facts (fakes only), and no live run was made for any slice.
