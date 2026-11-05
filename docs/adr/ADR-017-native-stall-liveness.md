# ADR-017: Native stall liveness (report-only)

Date: 2026-11-04. Status: **ACCEPTED (option (a) only), 2026-11-05, maintainer
decision recorded by the Planning Desk** ([D-ADR017-A](../decisions/D-ADR017-A.md)).
Options (b) and (c) are not accepted: (b) needs a later amendment with live
evidence; (c) stays rejected.
Amends nothing yet. Must preserve ADR-010 and ADR-011 as accepted.

## Problem

R3a found an execution that is never resolved. OpenCode 1.19.4 did not persist
the provider stream error on the assistant message, and the abort added only
`time.completed`. Without a terminal signal, the Bridge correctly reported
`unknown`. However, it kept the session guard until the Bridge restarted. A
restart drops the in-memory idempotency records (ADR-010), so old keys must
never be reused afterwards.

This ADR decides what the Bridge exposes about such stalls, and which kinds of
guard release are acceptable.

## 1. Evidence

### R3a (`docs/evidence/R3A_EVIDENCE_2026-11-03.md`)

Findings, item 1 (quoted):

> 1. OpenCode 1.19.4 does not persist provider stream errors on the assistant message; abort adds only
>    `time.completed`. Bridge correctly stays `unknown`, but recovery required a Bridge restart
>    (liveness gap; candidate R5 item "native stalled").

Verdict rows (quoted):

> | Uncertain execution handling (provider failures) | PASS: no success inferred, no replay, guard held (exec c76148d6…, d846680d…) |
> | Interrupt path | Abort sent and confirmed; Bridge did not promote without terminal evidence (exec c76148d6…) |

### ADR-010 (quoted)

> | Close/timeout/submission error | Unknown outcome retains session guard; no blind retry; transport DELETE/expiry is not native cancellation |

> Execution/key/parent-ID mappings remain memory-only. Shutdown/crash releases OS
> locks and loses these mappings; native execution can continue. The next process
> cannot recover an old execution ID or tell a reused key from a fresh request.

> Old keys must not be reissued after restart. If reconciliation is impossible, remain read-only.

### ADR-011 (quoted)

> Require the exact native user ID to be present, assistant parentID and native
> session to match, positive native completed timestamp, final stop/end_turn or
> named structured native error, no unfinished correlated assistant, idle/absent
> native status, and a successful permissions read with no matching pending
> permission. Idle,204 or elapsed time alone never suffice.

> Inconclusive evidence retains unknown and the guard. A native permission wait
> remains explicit waiting_for_permission, including after expiry. No native
> permission approval or automatic Abort is added.

### Current code (`main` `34541b1`)

| Behavior | Location |
| --- | --- |
| The guard is reserved before any native I/O: `m.busy[session] = id`. A second key gets `SESSION_BUSY`. | `internal/execution/manager.go:202-208` |
| The guard is released only on admission failure. | `manager.go:228-233` (`release`) |
| The guard is released when the request was provably never submitted. | `manager.go:331-353` (`finalizeUnsubmitted`) |
| A Prompt error marks the execution `SUBMISSION_UNCONFIRMED` and keeps the guard. Prompt is never retried. | `manager.go:397-402` |
| The background monitor runs for 5 min (`New`). On expiry the execution becomes `MONITOR_EXPIRED` and the guard is kept. | `manager.go:103`, `:414-426` |
| On shutdown, unresolved executions are marked `MANAGER_CLOSED`. Records are lost with the process. | `manager.go:108-126` |
| The guard view used by discovery. | `manager.go:471-490` (`SessionGuard`) |
| Bounded read-side reconcile. | `internal/execution/reconcile.go:173` (`probe`) |
| Terminal proof needs: correlated user found, no unfinished correlated assistant, a proof message, and native status absent or `idle`. Only then is the guard released, atomically. | `reconcile.go:284-304` |
| Inconclusive evidence stays `unknown`. `busy` and `retry` are treated as `running`. | `reconcile.go:305-316` |
| Proof is `finish` = `stop` or `end_turn`, or a named `error` (`MessageAbortedError` → interrupted). `time.completed` alone is **not** proof. | `reconcile.go:319-339` |

**R3a stall shape in Bridge terms:**
- A correlated assistant has `time.completed > 0`, no `finish` and no `error`.
- `terminalState` returns `""`, so there is no proof. Native status is absent and there is no pending permission.
- The monitor expires and the execution becomes `MONITOR_EXPIRED`. Every later probe returns "native terminal evidence remains inconclusive".
- The guard is held for the lifetime of the process.

## 2. Observable native signals in OpenCode 1.19.4

Source: `anomalyco/opencode` tag `v1.19.4`, revision
`148842ef7cb1bc34192b3fff57d0f9721ab9c801`. Paths are relative to that
repository.

| # | Signal | Upstream source | What it tells us | Status |
| --- | --- | --- | --- | --- |
| N1 | `GET /session/status` returns only non-idle sessions. `set(idle)` deletes the entry, and `get()` returns `idle` for an absent session. | `packages/opencode/src/session/status.ts:30-48`; handler `packages/opencode/src/server/routes/instance/httpapi/handlers/session.ts:77-79`; route `.../groups/session.ts:80,121` | Absent means idle or never tracked. It does **not** mean finished. | Verified (source) |
| N2 | The status union is `idle`, `busy`, or `retry {attempt, message, action?, next}`. | `packages/schema/src/session-status-event.ts:9-32` | `retry` with `next` is positive evidence that work is still scheduled. | Verified (source) |
| N3 | `busy` is set at each prompt-loop step and at stream start. | `packages/opencode/src/session/prompt.ts:1089`; `packages/opencode/src/session/processor.ts:653` | `busy` means a step is active. It does not prove progress. | Verified (source) |
| N4 | `retry` is set by the retry policy on stream failure. | `processor.ts:674-688` (`SessionRetry.policy`, `status.set({type:"retry",…})`) | Provider trouble that is being retried. | Verified (source) |
| N5 | `halt` sets `assistantMessage.error` (and `finish:"error"` for a context overflow without auto-compaction), publishes `session.error`, and sets status `idle`. | `processor.ts:613-639` | By the source, a stream error should land on the message. | Verified (source). This **conflicts with the R3a observation**; the cause is UNVERIFIED (see note). |
| N6 | `cleanup` stamps `time.completed` and persists the message. Unfinished tool parts become `status:"error"` with `metadata.interrupted:true`. | `processor.ts:553-611` (tool parts `:591-607`, completed `:609-610`) | `time.completed` is written on every exit path, so it is **not** a success or terminal-kind signal. | Verified (source) |
| N7 | A finished step sets `finish = reason` and writes a `step-finish` part. | `processor.ts:452-470` | `finish` is the completion-kind signal the Bridge already uses. | Verified (source) |
| N8 | On interrupt, `finalizeInterruptedAssistant` sets `error ??= AbortedError` and `time.completed`, but returns early if `time.completed` is already set. It is attached via `onInterrupt` to `processor.create(...)` and to another step effect. | `prompt.ts:1203-1211`, `:1219`, `:1332` | It does not cover every interrupt point. This is a plausible route to "completed without error". | Code verified; whether it explains R3a is UNVERIFIED |
| N9 | `POST /session/:id/abort` → `SessionPrompt.cancel` → `SessionRunState.cancel`. With no runner, it sets `idle` directly. | `.../handlers/session.ts:232-233`; `prompt.ts:152-154`; `packages/opencode/src/session/run-state.ts:77-86` | Abort is acknowledged without any message evidence. | Verified (source) |
| N10 | Assistant message schema: `time.created`, optional `time.completed`, optional `error`, optional `finish`. | `packages/schema/src/v1/session.ts:453-486` | Absence of `finish` and `error` is representable and valid. | Verified (source) |
| N11 | Part timestamps: text parts `time.{start,end}`; tool parts `state.time.{start,end}`. | `processor.ts:569-573`, `:597-606` | The last part timestamp is a usable "last native progress" clock. | Verified (source) for the shapes written here; other part types UNVERIFIED |
| N12 | `GET /event` SSE stream with `session.status`, `session.idle` (deprecated), `message.updated`, `message.part.updated`, `message.part.delta` and `session.error`. | `.../httpapi/groups/event.ts:8`; `packages/schema/src/session-status-event.ts:35-49`; `packages/schema/src/v1/session.ts:597,613,633,652` | Push-based progress, including transient `session.error` that may not be persisted. | Verified (source). The Bridge does not consume these for executions today; delivery and ordering guarantees are UNVERIFIED. |

**Note on N5 and N8.** Static reading suggests that errors and aborts are
normally persisted on the message. The R3a live run observed otherwise. This
ADR does not depend on the root cause: a message that is "completed without
`finish`/`error`" is a valid schema state (N10) and must be handled. The root
cause stays UNVERIFIED until it is reproduced on a disposable instance.

**No native signal proves "dead".**
- Absent status means idle *or* never tracked.
- `time.completed` is written on every exit.
- `retry.next` can lie in the future.
- A process restart of OpenCode clears the in-memory status map: `status.ts` uses `InstanceState`.

## 3. Options

### (a) Report-only liveness fields

Add optional, read-only fields to execution status and result. They are
computed from the same bounded probe and never change `state` or the guard:

- `liveness.native_status`: `busy`, `retry`, `absent`, or `unavailable`. For
  `retry`, also `retry_attempt` and `retry_next_at`.
- `liveness.last_native_progress_at`: the maximum over the correlated
  assistant's `time.created` and `time.completed` and its part timestamps (N10,
  N11).
- `liveness.stall_suspected`: a boolean, plus `stall_reason`, one of:
  - `COMPLETED_WITHOUT_TERMINAL_EVIDENCE`: the R3a shape. The correlated
    assistant has `time.completed > 0`, no `finish` in {stop, end_turn}, no
    named error, native status absent, and no pending permission.
  - `NO_CORRELATED_ASSISTANT`: the user message exists, but there is no
    correlated assistant and native status is absent.
  - `NO_PROGRESS`: status is `busy`, or `retry` with `next` in the past, and
    there has been no progress for longer than a configured report threshold.

Risks:
- False suspicion, for example a long tool run with no part updates. This is
  acceptable because suspicion is advisory only.
- Clients may misread suspicion as failure. Mitigation: the contract states
  `stall_suspected` is never a terminal state and never success.
- More native reads per probe: none. The probe already reads history and status.
- Parsing part timestamps adds code that touches untrusted native JSON. It
  needs bounded, total parsing and must ignore unknown parts.

### (b) Operator-explicit guard release

Add a new explicit tool or flag (for example, `release_session_guard`). It is
authorized like interrupt, needs a separate grant, and writes an AUDIT-01 line.
It would release the guard for one execution that is `unknown` and has
`stall_suspected`, after a fresh probe re-confirms the suspicion. The execution
stays `unknown`, with a new unresolved cause `GUARD_RELEASED_BY_OPERATOR`. It is
never marked failed, completed or interrupted. Its key and fingerprint stay
recorded: a replay of the same key still returns this execution, and the key is
never accepted for new work.

Risks:
- Releasing the guard admits a new prompt into a session whose old turn may
  still be alive, for example if the stall was a false positive. That could
  lead to concurrent native work and confusing history.
- This only helps inside a live process. After a restart, ADR-010's fenced
  procedure still applies.
- It adds a new mutation surface, so it needs its own authorization model and
  audit contract.
- Operator error: releasing in the wrong session. Mitigation: require the exact
  execution ID and current native evidence.

### (c) Time-based automatic release

Release the guard automatically after a stall persists for T.

This is **rejected**:
- It infers an outcome from elapsed time. ADR-011 says "Idle,204 or elapsed time
  alone never suffice", and "inconclusive evidence retains unknown and the
  guard".
- The next caller could submit a new prompt while the old turn is still running
  (N3/N4 show `retry` with a future `next`). That is duplicate or concurrent
  native work without any human decision, which defeats the duplicate-prompt
  safety that the guard exists for.
- No native signal proves death (section 2), so any T is a guess.
- Automatic release can also tempt clients to "retry with a new key". ADR-010
  forbids treating a timeout as permission to use a new key.

## 4. Recommendation for the MVP

**Adopt (a) only.** Defer (b) to a separate amendment that requires live
evidence, and reject (c).

Invariants this keeps:
- No automatic resubmission, and no automatic Abort.
- No success is inferred. `stall_suspected` never changes `state`, never sets
  `ended_at`, and never releases the guard.
- Idempotency keys are never reused, including across restarts. Liveness fields
  are process-local, like all execution records.
- Recovery from a confirmed stall stays the ADR-010 fenced procedure: fence
  writers, reconcile native history, restart with new keys only.

**What R5 must record** for every stalled execution it encounters:
- execution ID and session ID
- `state`, `unresolved.reason`, and `monitor_ended_at`
- the `liveness` block, verbatim
- the native evidence used: status, and the correlated assistant's `finish`,
  `error` and `time.completed`
- any operator action taken, with time and actor
- confirmation that no prompt was resubmitted and no key was reused
- the Bridge PID and lifetime boundary, if a restart was used

The R5 supervisor may surface `stall_suspected` to the human. It must not act
on it automatically.

## 5. Test plan for (a): unit only, no live services

All tests use the existing fake runner in `internal/execution`.

1. **R3a shape.** The assistant has `completed > 0`, no `finish`, no `error`,
   status is absent, and there are no permissions.
   - Expect `stall_suspected=true` with `COMPLETED_WITHOUT_TERMINAL_EVIDENCE`.
   - `state` stays `unknown` (or `running` before the monitor ends), and the
     guard stays held (`SessionGuard` → held).
   - `SESSION_BUSY` is still returned for a new key.
2. **Real terminal proof wins.** The same shape with `finish:"stop"` becomes
   `completed`, the guard is released, and `stall_suspected` is absent.
3. **Retry is not a stall.** `{type:"retry", next: future}` gives
   `native_status=retry` with attempt and `next_at`, and no suspicion.
4. **NO_PROGRESS threshold.** Status `busy` with an injected clock beyond the
   threshold sets suspicion. Below the threshold there is none. Advancing a part
   timestamp clears it.
5. **Pending permission takes precedence.** The state is
   `waiting_for_permission` and there is no suspicion.
6. **No native mutation.** Assert the fake runner records zero Prompt and zero
   Abort calls across probes with suspicion.
7. **Replay is unchanged.** The same key and payload return the same execution,
   including its liveness block. A different payload is still
   `IDEMPOTENCY_CONFLICT`.
8. **Restart.** A fresh manager knows nothing about the old execution
   (`NOT_FOUND`). The existing regression test that documents fresh-manager
   resubmission stays unchanged, with no new acceptance of old keys.
9. **Hostile native JSON.** Oversized, unknown, or malformed parts and
   timestamps are ignored without panic. `last_native_progress_at` is never in
   the future of the probe time.
10. **Contract.** JSON compatibility: the new fields are optional, and old
    parsers still decode the output (as in ADR-011's compatibility tests).
11. **Race.** `go test -race -count=20 ./internal/execution` with concurrent
    probes and status reads.

## Out of scope

- Option (b) and any new mutating tool.
- Consuming `/event` for executions.
- Reproducing the R3a root cause (N5/N8) on a disposable OpenCode instance.
- Changes to ADR-010/011 text. If (a) is accepted, it adds optional output
  fields only.
