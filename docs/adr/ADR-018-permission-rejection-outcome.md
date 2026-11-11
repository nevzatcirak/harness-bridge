# ADR-018: Native permission rejection outcome

Date: 2026-11-06. Status: **ACCEPTED: option (d) built on (a)** (maintainer,
2026-11-06, via Planning Desk; issue #97). Option (b) is deferred and option (c)
is rejected. §5 (P3, pending permission detail) was not decided and stays
**PROPOSED**. Must preserve ADR-010, ADR-011 and ADR-017 (option (a)) as
accepted. Implementation: see "Implementation (option (d))" below.

ADR numbering: ADR-015 and ADR-016 are referenced in `docs/ROADMAP.md` as
PROPOSED but are not in the repository. ADR-018 is the next unused number.

**Amendment 2026-11-11 (maintainer decision D10: approved; text alignment
only, no behavior change).** PR #128 changed `internal/execution/reconcile.go`
so that an explicit native `idle` entry without terminal proof is treated
exactly like an absent entry: `unknown`, `OBSERVATION_UNAVAILABLE`, then
`MONITOR_EXPIRED`, never `running` (R83-REVIEW #86 finding 3). The behavior
now lives at `reconcile.go:376` (`!exists || status.Type == "idle"` on the
terminal-proof path) and `:405-407` (the unresolved branch for absent, idle
or unsupported status). The original text of this ADR, written at `fb298ed`,
said `running` in two places; both are aligned above: the evidence-table row
`TestF7PermissionRejectIdleStatus` in §2, and the "Out of scope" bullet about
the `idle`-without-proof report. The test
`TestF7PermissionRejectIdleStatus` asserts the new outcome
(`internal/execution/permission_reject_test.go:129-165`). The accepted decision
(option (d) built on (a)), the kept invariants and §5 are unchanged. Nothing
else in this ADR is edited.

## Problem

Finding F-7 (R5b, 2026-11-06, P2), on writer `692992d`: a human rejected a
native permission. OpenCode then recorded the assistant message as completed
with `finish: "tool-calls"`, and set the bash tool part to `status: "error"`
with the text "The user rejected permission to use this specific tool call."
The agent took no further step.

The Bridge kept the execution `unknown` (`OBSERVATION_UNAVAILABLE`, then
`MONITOR_EXPIRED`) and held the guard. That is safe, but the supervisor cannot
tell a rejection apart from uncertainty.

This ADR decides what the Bridge should report in that case, and on what proof.

## 1. Upstream behavior (OpenCode v1.19.4, `148842ef7cb1bc34192b3fff57d0f9721ab9c801`)

All paths below are in `anomalyco/opencode` at that commit.

### a. What `reject` does, and how the tool part stores it

- `packages/opencode/src/permission/index.ts:121-127`: on `reply === "reject"`,
  the pending ask fails with a typed error:
  - `PermissionV1.CorrectedError({ feedback })` when the reply carries a
    `message`;
  - otherwise `PermissionV1.RejectedError()`.
- `index.ts:129-138`: the same reject also fails **every other pending request
  of that session** with `RejectedError`. Each one publishes its own
  `permission.replied` with `reply: "reject"`.
- `index.ts:54-60`: when the instance is disposed, its finalizer fails all
  pending requests with the **same** `RejectedError`. No human reply is
  involved.
- `packages/core/src/v1/permission.ts`, the error definitions:
  - `:7-11`: `RejectedError`, tag `PermissionRejectedError`, message "The user
    rejected permission to use this specific tool call."
  - `:13-19`: `CorrectedError`, tag `PermissionCorrectedError`, message "The
    user rejected permission … with the following feedback: …"
  - `:21-27`: `DeniedError`, tag `PermissionDeniedError`. This is a
    config-rule deny, with no human involved.
- `packages/opencode/src/session/tools.ts:81-89`: a tool asks for permission
  with `tool: { messageID, callID }` and `Effect.orDie`.
- `packages/opencode/src/session/processor.ts:186-199`: `failToolCall` writes
  the tool part as follows:
  - `state.status = "error"`;
  - `state.error = errorMessage(error)`, which is the message string only
    (`packages/tui/src/util/error.ts:136-156`).
  - **The error tag and type are not persisted.**

### b. Does the loop stop, and what `finish` does the message carry?

- `processor.ts:647` sets
  `ctx.shouldBreak = experimental.continue_loop_on_deny !== true`. The option
  is defined at `packages/core/src/v1/config/config.ts:179-181`, "Continue the
  agent loop when a tool call is denied".
- `processor.ts:200-201`: **only** `PermissionV1.RejectedError` (or a
  `Question.RejectedError`) sets `ctx.blocked = ctx.shouldBreak`.
  `CorrectedError` and `DeniedError` never block.
- `processor.ts:694`: `blocked` makes `process()` return `"stop"`.
- `packages/opencode/src/session/prompt.ts:1319`: `"stop"` breaks the loop.
- `processor.ts:457`: the message's `finish` is the provider's step-finish
  reason. For a tool step this is `"tool-calls"`.
- `processor.ts:609`: cleanup sets `time.completed`.
- **No message `error` is set.**

The cases:

| Case | Loop | Last assistant message |
|---|---|---|
| `reject` without message, `continue_loop_on_deny` unset (R5b) | stops | completed, `finish: "tool-calls"`, no error |
| `reject` without message, `continue_loop_on_deny: true` | continues; the model sees the rejection text as the tool result | a later message ends with the model's own finish (e.g. `"stop"`) |
| `reject` with message (`CorrectedError`) | continues; the feedback goes to the model. Exception: the reply still cascade-rejects every other pending request of the session with plain `RejectedError` (`index.ts:129-138`); if one of those belongs to a tool call in the same step, `failToolCall` sets `blocked` (`processor.ts:200-201`) and the loop stops | as above, unless a cascade-rejected request stops the loop |
| config `deny` (`DeniedError`) | continues, unless another pending request of the session is cascade-rejected in the same step (as in the row above) | as above |

- `prompt.ts:1111-1130`: on a later loop iteration, the loop exits only when
  `finish` is not `"tool-calls"` or `"unknown"`. A rejected message therefore
  never satisfies that exit check; the R5b stop comes only from `"stop"` at
  `:1319`.

### c. Published events, and whether they can be seen after the fact

- `packages/schema/src/v1/permission.ts:61-66` defines:
  - `permission.asked`, carrying the request including `tool.messageID` and
    `tool.callID` (`:34`);
  - `permission.replied`, carrying `{ sessionID, requestID, reply }`.
- Neither definition passes `durable` (`packages/schema/src/event.ts:42-69`:
  `durable` is optional). They are therefore **not durable**. They reach only
  live `/event` subscribers (`packages/opencode/src/event-v2-bridge.ts:35-45`).
- `permission.replied` does not carry the message, so it cannot tell
  `RejectedError` from `CorrectedError`.
- HTTP after the fact:
  - `GET /permission` lists **pending** requests only
    (`packages/opencode/src/server/routes/instance/httpapi/groups/permission.ts:21-30`);
  - a replied request is removed (`index.ts:114`);
  - there is no reply-history endpoint.
- Session status: an idle session is deleted from `/session/status`
  (`packages/opencode/src/session/status.ts:42-44`), so after the stop the
  session reads as **absent**.

### d. Can persisted data alone distinguish a rejection?

**No.** The persisted facts are:
- a completed message with `finish: "tool-calls"` and no error;
- a tool part `status: "error"` whose `error` string equals the
  `RejectedError` message.

That string is not authoritative:
- instance disposal (`index.ts:54-60`) produces the identical string with no
  human reply;
- a reject cascades to other pending requests of the session (`index.ts:129-138`);
- the string is ordinary error text. Any tool, plugin or MCP server that throws
  an error with the same message produces an identical persisted part. Taking
  it as authority would treat untrusted tool output as authority.
- The typed tag is never persisted, and the `permission.replied` event is not
  durable.

What distinguishes the case is a live `permission.replied` (`reject`)
observed for a request whose `tool.messageID` and `tool.callID` match the
errored part. Even that cannot tell `RejectedError` from `CorrectedError`. It
also cannot show whether `continue_loop_on_deny` was set.

## 2. Current behavior on `main` (`fb298ed`)

`internal/execution/permission_reject_test.go` (fake runner, no live services)
reproduces the R5b sequence:
1. the execution is admitted;
2. native is busy with a pending permission, so the execution is
   `waiting_for_permission`;
3. the permission disappears;
4. an assistant message completes with `finish: "tool-calls"`, carrying the
   rejected bash part (`status: "error"`, rejection text);
5. native status is absent.

| Test | Main outcome |
|---|---|
| `TestF7PermissionRejectStaysUnknown` | **Still `unknown`.** `OBSERVATION_UNAVAILABLE` before monitor expiry, `MONITOR_EXPIRED` after. `ended_at` unset, error is the generic `UNKNOWN_OUTCOME: …`. Guard held, 1 Prompt, 0 Abort. New compared with `692992d`: the ADR-017 (a) block reports `stall_suspected: true` with `COMPLETED_WITHOUT_TERMINAL_EVIDENCE` (`internal/execution/liveness.go:176`). The supervisor still sees nothing that names a rejection. |
| `TestF7PermissionRejectIdleStatus` | Defensive, because OpenCode deletes idle entries. With native `idle` present and no proof, main (since PR #128) treats the entry like an absent one: `unknown`/`OBSERVATION_UNAVAILABLE`, liveness `native_status: "absent"`, until monitor expiry; then `unknown`/`MONITOR_EXPIRED`. Guard held. *(Amended 2026-11-11: the table originally recorded `running` at `fb298ed`; see the Amendment note.)* |
| `TestF7PermissionRejectThenContinueCompletes` | Continue case (feedback or `continue_loop_on_deny`): a later assistant message with `finish: "stop"` is promoted to `completed`, and the guard is released. No record remains that a permission was rejected on the way. |

Why: `terminalState` (`internal/execution/reconcile.go:404-424`) accepts only
`finish` `stop`/`end_turn` or a named message error. `"tool-calls"` yields no
proof (`reconcile.go:332, 389-401`). Neither ADR-017 liveness (#56) nor F-3
bounding (#67) changes this path; liveness only adds the report.

Mutation checks:
- treating `"tool-calls"` as completed fails tests 1 and 2;
- a naive "rejection text ⇒ `failed`" mapping fails all three. In the continue
  test it finalizes `failed` before the native loop has continued. This is the
  late-completion hazard in §3 (b).

**What main already sees about permissions (the P3 gap):**
- The execution path reads `GET /permission` through `Runner.Permissions`.
  It keeps only `sessionID` (`internal/adapters/opencode/client.go:57-59`).
  Status can say `waiting_for_permission`, but not which tool or pattern is
  pending.
- `internal/adapters/opencode/permission_observation.go` already builds a
  safe projection, `ObservedPermission` (`permission_observation_types.go:54-63`):
  - tool message and call IDs;
  - `Kind`, `Tool`, `ScopeDigest` and `PatternCount`, never raw patterns;
  - `State`, `Attribution`;
  - `ObservedReply`, the native `once`/`always`/`reject`, filled only from a
    live `permission.replied` event (`:526-534`).
- It is not wired into the execution manager or MCP. Only tests construct it.

## 3. Options

### (a) Keep `unknown`

No change. This is safe and consistent with ADR-010 and ADR-011: no success or
failure is inferred, the guard is held, nothing is resubmitted. The cost is
F-7 itself: the supervisor sees the same unknown and stall hint for a
rejection as for a lost stream.

Recovery stays the ADR-010 fenced procedure.

### (b) Terminal `failed` with `error_code: PERMISSION_REJECTED`, only on proof

The proof predicate: **every** element below is required. Any missing,
unreadable or contradictory element means no promotion; the result stays
(a).

1. **ADR-011 correlation, unchanged**:
   - exact native user message ID;
   - matching `parentID` and session;
   - workspace and cwd identity;
   - no unfinished correlated assistant;
   - status idle or absent;
   - a successful permissions read with no pending request for the session.
2. **The latest correlated assistant message**:
   - has `time.completed > 0`;
   - has `finish == "tool-calls"`;
   - has no `error`;
   - has no later correlated assistant message.
3. **That message has exactly one tool part with `state.status == "error"`.**
   Its `state.error` must be byte-equal to the `RejectedError` message;
   neither a prefix nor the `CorrectedError` text qualifies. No tool part may
   be `pending` or `running`.
4. **A live `permission.replied` with `reply: "reject"`** must have been
   observed by this process's `PermissionObserver` for a request whose
   `permission.asked` was:
   - observed in the same observer generation;
   - attributed `verified` to this execution;
   - for the same `tool.messageID` and `tool.callID` as the part in item 3.
   The observer must also show no gap, fence, stale or `observation_uncertain`
   status between the ask and the probe.
   Note: a request rejected by cascade (`index.ts:129-138`) also gets its own
   `permission.replied` with `reply: "reject"`, so an observed reject reply
   does not by itself show that a human rejected that request directly.
5. **The native loop has ended.** Items 1 and 2 must be read after item 4,
   and the reads must show status idle or absent and no newer assistant
   message.

Consequences:
- **Guard release** is the ADR-011 atomic terminal commit under the run mutex.
- **Idempotency:** the key stays bound to the `failed` execution. A replay
  returns it and never resubmits (as V2 `finalizeUnsubmitted`). Keys are never
  reused across restarts.
- **ADR-010:** compatible only because the Bridge never sends the reply
  itself. The human replies natively, and the Bridge only observes. No
  automatic Abort and no native permission response. ADR-016 is untouched.
- **ADR-011 late completion and immutability:** a terminal state is final.
  - If `continue_loop_on_deny` is set, the proof can still hold for one
    probe before the next step starts. Item 5 narrows that window but cannot
    close it: native reads are not atomic, and the Bridge cannot read the
    config authoritatively.
  - A later native completion would then be hidden behind a wrong `failed`.
  - The `CorrectedError` and `DeniedError` cases never qualify, because
    item 3 requires the exact `RejectedError` text, and they continue.
- **Restart:** item 4 is process-local and in memory. After a Bridge restart
  the proof is impossible, which is correct, since persisted data alone cannot
  prove it (§1 d).
- **Contract:** CONTRACTS needs a new code `PERMISSION_REJECTED` in the
  error enum, plus a result field naming the rejected tool part. It also
  requires wiring `PermissionObserver` (an SSE lifecycle) into the manager.

### (c) A new execution state, e.g. `permission_rejected`

- **Terminal variant:** has the same proof needs and risks as (b), and adds
  an enum value that strict parsers reject.
- **Non-terminal variant** (holding the guard): it is (a) under another name,
  and still breaks strict state parsers.

Either way, MCP consumers and docs must change, with no safety gain over (b)
or (d).

### (d) Report-only rejection evidence (ADR-017 (a) style)

Keep `state`, `error`, `ended_at` and the guard exactly as in (a). Add an
optional, report-only block, for example
`permission_rejection: { observed_at, tool, tool_call_id, basis }`:
- **`basis: "native_event"`** when proof items 2–4 hold for the current probe.
  The live reply was observed and attributed as in item 4.
- **`basis: "persisted_text_only"`** when only items 2–3 hold. In this case
  the block is labeled untrusted: the text could come from a tool.

The block is computed from reads already made, plus the observer's in-memory
view. It never changes state or the guard, and the supervisor never acts on
it automatically. It answers F-7 for the human without inferring an outcome.

## 4. Decision (ACCEPTED 2026-11-06: (d) built on (a))

**Accepted: (d)**, built on (a) (maintainer, 2026-11-06, via Planning Desk).
(b) is deferred to a later amendment that requires live evidence; (c) is
rejected. This section was the recommendation; the reasons below are why it was
accepted.

Why:
- §1 d shows that persisted data cannot prove a rejection.
- The typed signal is a non-durable live event that loses the
  `CorrectedError`/`RejectedError` distinction.
- `continue_loop_on_deny` is invisible to the Bridge.
- A terminal mapping would therefore rest on a race-prone, process-local proof,
  and would conflict with ADR-011 immutability whenever the native loop
  continues.
- (d) gives the supervisor the distinguishing fact. It keeps every ADR-010,
  ADR-011 and ADR-017 invariant, and needs no new state or error code.

Kept invariants:
- no inferred success or failure;
- no resubmission;
- no automatic Abort;
- no native permission reply by the Bridge;
- guard released only by terminal proof or the ADR-010 fenced procedure;
- keys never reused.

Evidence needed before (b) is reconsidered: an R-run in which the observer
captures `asked` → `replied(reject)` → stop for one execution, with the
correlation in item 4, and a negative run with `continue_loop_on_deny: true`.

## 5. Separate decision point: P3, pending permission detail in status (PROPOSED)

**Status: PROPOSED, not decided (2026-11-06). Not implemented.**

Today `waiting_for_permission` does not say what is pending. Proposal: when
the state is `waiting_for_permission`, expose the `ObservedPermission` safe
projection for the session. That means:
- `tool`, `kind`, `pattern_count` and `scope_digest`;
- `request_id`;
- `attribution`.

Never expose raw patterns, metadata or native text: patterns can contain
commands or paths.

This needs the same observer wiring as (d) and can ship before or with it.
It is report-only and grants no ability to reply. Decide it independently of
§4.

## Test plan (if (d) is accepted; unit, fake runner)

- `permission_rejection` is present with `basis: "native_event"` only when
  items 2–4 hold, and `basis: "persisted_text_only"` otherwise. State, error,
  `ended_at` and the guard are unchanged in every case (extending the
  `TestF7…` tests).
- Each item removed or contradicted in turn: a later assistant message, a
  second errored part, a `CorrectedError` text, a mismatched `callID`, an
  observer gap or fence, a pending permission. Each yields no
  `native_event` basis.
- The block never triggers an Abort or a reply; replay is unchanged.
- No extra native reads beyond the observer stream.

## Implementation (option (d))

- `internal/execution/rejection.go`: `PermissionRejection` (`observed_at`,
  `tool`, `tool_call_id`, `basis`) on the execution view of
  `get_execution_status` and `get_execution_result`. It is computed in the
  probe from the reads already made (items 2-3) plus an optional in-memory
  `RejectionWitness` (item 4). It is assigned only to that field.
- `basis: "native_event"` requires the witness to confirm the exact
  (session, execution, assistant message, tool call). With no witness, or a
  witness that answers false, the basis is `persisted_text_only`. When items
  2-3 do not hold, or a permission is pending, or the execution is terminal,
  there is no block.
- `internal/adapters/opencode`: `PermissionObserver.VerifiedRejection` is the
  witness view. It reads only the observer's memory and answers true only for
  one verified, reject-resolved, conflict-free entry while the observer is
  unfenced, not streaming and `available`.
- **Limitation:** the observer is not wired into the running Bridge, and its
  attribution cannot be `verified` while `PermissionScope.NativeEpoch` is
  empty. Until both change, production reports only `persisted_text_only`.
  Wiring the observer is a separate task: it adds the observer's own `/event`
  and snapshot reads.

## Out of scope

- Any native permission reply or approval by the Bridge.
- Any guard release without terminal proof.
- Any change to ADR-010, ADR-011 or ADR-017.
- The `idle`-without-proof `running` report in §2. That state came from
  `reconcile.go:396-399` at `fb298ed` (405-410 after the option (d) change)
  and was unreachable with OpenCode 1.19.4 status semantics; it was recorded
  only as a finding. It was fixed by PR #128 (see the Amendment note); this
  bullet is kept as the original scope statement.
