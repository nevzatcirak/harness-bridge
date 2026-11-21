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

## Amendment A2 (ACCEPTED, 2026-11-21, maintainer decision D32): stall and degradation taxonomy v2

Status: **ACCEPTED (2026-11-21, maintainer decision D32, recorded in issue #117).** This amendment
answers the maintainer need of 2026-11-21: network drops, a provider that fails or keeps retrying, and
a hung or long-running child tool must be distinguishable so the supervisor can wait, interrupt or
follow up. It is additive to the accepted A1 (option (a)); the implementation slices follow this
amendment. It preserves ADR-010/011: **report-only**; no automatic Abort, resubmission, guard release,
new key or inferred success; recovery from a confirmed stall remains the ADR-010 fenced procedure. The
maintainer decided every decision question in D32 (Q5 = Option 2's bounded free-text fallback; Q7
is factual, UNVERIFIED live behaviour); A2.14 records the decisions and Q7 as such.

*Revision note (for the delta review): the first draft's claims about OpenCode network attribution,
the `send_prompt` override, `HARNESS_UNREACHABLE`, retry semantics vs A1 test 3, a stale running tool
and the dropped wedged-retry case were wrong or overclaimed. This revision keeps A1's wedged-retry
`NO_PROGRESS` under Variant A, defines `HARNESS_UNREACHABLE` by the failure reason class (not the
shared `failed()` helper), adds the `retry_attempt_since` memory, keeps A1's `NO_PROGRESS` for a
running tool (the `TOOL_RUNNING_LONG` is an explanation, never a suppression), accepts the D32 Q5
bounded free-text fallback, records the A2.14 decisions, and corrects the `RUN_MAX` wording and the
remaining review P3s. The code citations were re-checked at `cdaf5bd` (main) and at the PR head.*

### A2.1 Pinned sources and what the Bridge reads today

**OpenCode 1.19.4**, `anomalyco/opencode` tag `v1.19.4`, revision
`148842ef7cb1bc34192b3fff57d0f9721ab9c801` (the revision A1 §2 uses). Facts read at that revision on
2026-11-21 (paths relative to the repository root):

| Fact | Source | Status |
| --- | --- | --- |
| `session.status` retry carries `attempt`, `message`, `action{reason, provider, title, message, label, link?}`, `next` | `packages/schema/src/session-status-event.ts:13-28` | verified (source) |
| `RetryReason` is `"free_tier_limit" | "account_rate_limit" | (string & {})`: an **open** string; the retry policy sets `action` only for the two usage-limit bodies, every other retry gets the free-text `message` only | `packages/opencode/src/session/retry.ts` (`RetryReason`, `retryable`, `policy`) | verified (source) |
| The pinned retry policy stops above `RETRY_MAX_RETRIES = 5`; text patterns classify a retryable error. An operator may raise the bound in other configurations (UNVERIFIED here) | `packages/opencode/src/session/retry.ts` | verified for the constant; no unbounded mode in the pinned source |
| Assistant error union: `ProviderAuthError{providerID,message}`, `UnknownError{message,ref?}`, `MessageOutputLengthError`, `MessageAbortedError`, `StructuredOutputError{message,retries}`, `ContextOverflowError`, `ContentFilterError`, `APIError{message,statusCode?,isRetryable,responseHeaders?,responseBody?,metadata?}` | `packages/schema/src/v1/session.ts` (`AssistantErrorSchema`, `Assistant.error`) | verified (source) |
| A `retry` part is in the assistant part union: `{type:"retry", attempt, error: APIError, time.created}`. Whether the server persists it **while** a retry is pending is UNVERIFIED (Q7) | `packages/schema/src/v1/session.ts` (`RetryPart`) | verified (source) for the schema |
| Tool state: `pending` (no time), `running{input,title?,metadata?,time.start}`, `completed{…,time.start/end}`, `error{…,time.start/end}`; a running tool has **no output timestamp** | `packages/schema/src/v1/session.ts` (`ToolStateRunning`, `ToolPart`) | verified (source) |
| `/event` carries `session.status`, `session.error`, `message.part.updated`, `message.part.delta` (A1 N12) | `packages/schema/src/session-status-event.ts`, `…/v1/session.ts` | verified (source); not consumed by the Bridge |
| The Bridge reads `Status{type,attempt,next}` only; `message`/`action` are dropped | `internal/adapters/opencode/client.go:97-104` | verified (code) |
| Liveness progress uses assistant `time.created/completed` and text/reasoning/tool part timestamps; unknown part types (including `retry`) are ignored | `internal/execution/liveness.go:80-115,151-170` | verified (code) |
| The assistant error **name** is read for terminal proof (`MessageAbortedError`→interrupted, any other named error→failed) | `internal/execution/reconcile.go:440-464` | verified (code) |
| `native_status=unavailable` is set by the shared `failed()` closure (`reconcile.go:229-235`) for **any** probe failure, by `IDENTITY_MISMATCH` (including the cwd mismatch at `:340-344`), and by an unrecognized status type (`liveness.go:139-140`). The **failure class** is the discriminator, not the helper; `OBSERVATION_UNAVAILABLE` alone is shared by the four native reads **and** the workspace identity-check timeouts (`:249,274,299`) and caller cancellation/expiry (`:282`). The current `failed(…, nil)` calls at `:244,258,262,266` discard the error, so S3 must thread it through and classify: `native_unavailable` (a native metadata/status/history/permissions read failed with a transport/connection error, a read timeout **including HTTP 408**, or a 5xx), `native_refused` (a native 401/403/404 or other 4xx incl. 429, a 3xx, an undecodable body — `client.go:205` — or an over-large response: reachable but unusable), `local_fs` (a workspace `errFSTimeout`), `canceled` (context/manager expiry, checked before classifying the read error), or `identity` (`IDENTITY_MISMATCH`, including the cwd mismatch). Only `native_unavailable` counts for A2.4 item 1 | `internal/execution/reconcile.go:229-359,440-464`; `internal/execution/liveness.go:139-140` | verified (code): A2.4 item 1 must define a **native-read failure class**, not the shared line range |
| The probe already reads status, history and permissions; A2 adds no native read | `internal/execution/reconcile.go:216-345` | verified (code) |

**Claude Code 2.1.325.** The runner already spawns the CLI with `--output-format stream-json
--verbose` (`internal/adapters/claudecode/argv.go:160-164`), so `system/api_retry` is already on the
stream. `DecodeLine` parses `system/init`, `result`, and assistant `tool_use` / user `tool_result`
blocks (`internal/adapters/claudecode/decoder.go:180-225`); `Block` carries the tool name and input
but **no tool_use id**, so S4 must add the id to pair parallel tools. The state machine ignores
other system subtypes (`state.go:218-244`). The event fields are from the public reference
`code.claude.com/docs/en/headless` (retrieved 2026-11-21), not the pinned binary (UNVERIFIED for 2.1.325):

| `system/api_retry` field | Type | Use |
| --- | --- | --- |
| `attempt` | integer from 1 | retry count |
| `max_retries` | integer | bound for this failure (may be lower than the session budget) |
| `retry_delay_ms` | integer | delay before the next attempt |
| `error_status` | integer or null | HTTP status; **null = no HTTP response** (connection-level) |
| `error` | enum string (documented, open in practice) | `rate_limit`, `server_error`, `authentication_failed`, `billing_error`, `invalid_request`, `max_output_tokens`, `unknown`; other observed/documented values include `overloaded`, `oauth_org_not_allowed`, `account_on_hold`, `model_not_found`, `cloud_credential_error` (map per A2.5) |
| `no_response` | object, optional (docs: v2.1.261+) | `{waited_ms, retry_wait_ms}`: a structured "no response in time" signal (better than `error_status=null`) |
| `session_id`, `uuid` | strings | identity |

The official page **documents** the silent first two `apiKeyHelper` 401/403 retries (v2.1.246+), so it
is not a third-party claim; it does not apply to Bridge-run executions, whose init verification
already requires `apiKeySource=none` (`internal/adapters/claudecode/state.go` `checkInit`). The
absence of `api_retry` is still not evidence of a healthy provider for a CLI run outside that check;
the ADR keeps the field set and 2.1.325 behavior UNVERIFIED.

### A2.2 What A1 cannot tell apart (gap table against the maintainer need)

| Case | A1 signal today | A2 report | Still impossible |
| --- | --- | --- | --- |
| Network down (OpenCode, while a prompt runs) | status `retry` with a **free-text** message (no `action` for a network failure), or `busy`, or `unavailable` | A **status-only** retry is `PROVIDER_RETRYING`/`degradation_reason`; the hint comes from the structured source and, for a status-only retry, the D32 Q5 bounded free-text fallback (`cause_source=bounded_text`; a text matching no closed pattern is `unknown`); a structured no-HTTP signal (assistant `APIError` without `statusCode`, a persisted `retry` part if Q7, or Claude `error_status=null`/`no_response`) is still the strongest | The D32 Q5 fallback distinguishes network from provider for the common status-only retry (bounded, redacted, closed set, report-only); a text that matches no pattern stays `unknown`; Q7 remains UNVERIFIED and no longer blocks |
| Provider retrying | `native_status=retry`, attempt/next; no reason; a retry whose `next` is past with no progress past the threshold is A1 `NO_PROGRESS`/`stall_suspected` | `PROVIDER_RETRYING` (degradation), attempt/next/max, `retry_overdue` when the retry is past due and the attempt is not advancing, with `PROVIDER_RETRY_OVERDUE` (D32 Q11) when that is true; **a wedged retry (next past, no progress > `no_progress_s`, including a running part) keeps A1's `NO_PROGRESS`/`stall_suspected`; `retry_overdue` may or may not accompany it** (A2.4 items 5/7); the `tool`/`TOOL_RUNNING_LONG` explanation is additional; `cause_hint` from the structured/fallback source | The total retry budget is not exposed by OpenCode (the pinned constant is 5, but config-dependent, UNVERIFIED); `retry_overdue` does not prove a wedge and is not implied by `NO_PROGRESS`; the A1 `NO_PROGRESS` signal is not dropped |
| Provider hard error | terminal `failed` if the assistant carries a named error; otherwise `unknown`/`MONITOR_EXPIRED` (R3a) | Terminal unchanged; if no terminal proof, a **narrow** `PROVIDER_ERROR` from a typed error (assistant error without `time.completed`, or a `retry` part) with a cause | Whether an error without terminal evidence means the turn ended is UNVERIFIED (R3a N5/N8); R3a itself persisted **no** error |
| Hung tool child | `busy` + part timestamps; A1 `NO_PROGRESS` at 2 min (also for a legitimate long tool) | A1's `NO_PROGRESS`/`stall_suspected` at `no_progress_s` **even with a running tool** (the boolean-only client keeps the A1 signal); the `tool` object gives the name/elapsed, and at `tool_long_s` the `TOOL_RUNNING_LONG` explanation (never a suppression); a stale running part with status absent is not a running tool | No native liveness inside a tool (no output timestamp); a stale running part after a harness restart (status absent) is **not** reported as "tool running" — it stays with A1's shape |
| Legitimately long tool (build) | same | the same A1 `NO_PROGRESS`, plus the `tool`/`TOOL_RUNNING_LONG` explanation and the operator threshold; the tool name/elapsed time let a human decide | The Bridge must not guess intent; a long build still shows a stall plus its explanation |
| Model thinking a long time | `busy` + no completed part; `NO_PROGRESS` at 2 min | `NO_PROGRESS` (no distinct signal) | Neither harness distinguishes thinking from a hung model/provider call |
| Bridge→harness connection lost | `native_status=unavailable` (also set on identity mismatch/unknown type) | `HARNESS_UNREACHABLE` only from the **native-read failure class** (A2.4 item 1: class `native_unavailable` only; `native_refused`, `local_fs`, `canceled` and `identity` excluded), after the window, with `unavailable_since` stored on the run | The error surface does not name the local/remote cause; no retry proves the old turn ended |

No case gains an automatic action.

### A2.3 v2 fields (additive, optional, closed sets, report-only)

All A1 fields keep their names and meanings for the A1 reasons; the A2 reasons below are additive. New optional fields:

| Field | Type / values | Meaning |
| --- | --- | --- |
| `degradation_reason` | `PROVIDER_RETRYING`, `PROVIDER_RETRY_OVERDUE` (when `retry_overdue` is true), `PROVIDER_ERROR` (Variant A) | A provider problem; a **healthy** retry is not a stall. An overdue retry uses the separate `PROVIDER_RETRY_OVERDUE` reason (D32 Q11); a **wedged** retry is also `NO_PROGRESS`/`stall_suspected` (A2.4 items 5/7). Variant B moves these to `stall_reason` — not chosen by D32 |
| `degraded` | bool (Variant A) | **only an alias**: true iff `degradation_reason` is present; never a separate state and never a terminal state; read `degradation_reason` for the reason |
| `stall_reason` | A1 values plus `HARNESS_UNREACHABLE` (and `PROVIDER_*` in Variant B) | A stall reason; in Variant A the A1 reasons keep A1's meaning (including the wedged retry and a `busy` execution with a running tool); `HARNESS_UNREACHABLE` is the one new boolean-true case. A `TOOL_RUNNING_LONG` is **not** a `stall_reason` — it is the `explanation` below, so it never suppresses or replaces `NO_PROGRESS` |
| `explanation` | `TOOL_RUNNING_LONG`, optional | A supplementary fact about a stall, not a reason: `native_status=busy` with a running tool at or after `tool_long_s` (and a `NO_PROGRESS` on a wedged retry with a live running part); the `tool` object gives the name/elapsed. It never changes `stall_suspected`, never suppresses `NO_PROGRESS`, and is display-only |
| `retry_overdue` | bool, optional | true when the current retry's `next` is past by more than `max(30 s, no_progress_s)` **and** the run's `retry_attempt_since` is at least `no_progress_s` old; a changed attempt (which resets `retry_attempt_since`) or a future `next` clears it. It is an **additional hint**: a wedged `NO_PROGRESS` may or may not carry it (the `NO_PROGRESS` needs only `next` past and no progress; the grace/attempt window can be longer) |
| `retry_attempt_since` | RFC3339, optional, on the run | First observation of the current retry attempt (Bridge clock; for Claude, the Bridge receive clock), stored on the execution record like `unavailable_since`, not in the per-probe block; cleared when the attempt number changes, the retry ends, or the status is no longer `retry`; the memory `retry_overdue` needs |
| `cause_hint` | `network`, `rate_limit`, `auth`, `server`, `unknown` | Coarse provider cause from the structured source or the D32 Q5 bounded fallback (A2.5); never the raw text |
| `cause_source` | `structured`, `bounded_text` (D32 Q5), `none` | Provenance; a structured field that maps to `unknown` is still `structured`; `bounded_text` only from the closed pattern set, and the matched text is never exported |
| `retry_max` | integer, optional | Claude `max_retries`; absent for OpenCode (not exposed) |
| `tool` | object, optional | `{tool_name, running_since}` of the oldest running tool of the newest correlated assistant; present when a running tool is present (a `busy` execution, or a wedged retry with a live running part); absent for a stale part |
| `tool.tool_name` | string ≤64 bytes, `[A-Za-z0-9._-]` or `other` | Native tool name, sanitized; untrusted display data, never authority |
| `tool.running_since` | RFC3339 (native `state.time.start`, never after `observed_at`) | When the running tool started |
| `unavailable_since` | RFC3339, optional | First observation of the current **native-read failure** window (Bridge clock), stored on the execution record, not in the per-probe block; cleared by a successful probe or by any non-counting failure (A2.4 item 1) |
| `thresholds` | object | `{no_progress_s, tool_long_s}` actually applied (operator-configured; D32 Q3). `tool_long_s` is the effective value for the running tool: the global default or a per-tool override (D32 Q10) |
| `progress_source` | `native`, `bridge` | OpenCode: `native`; Claude: `bridge` |

`tool.last_output_at` is **removed**: neither harness exposes a per-tool output timestamp (A1 N11 is
per-part `time.start/end`; a running tool has only `start`). A stale running part with status absent
is not a running tool, is not a `TOOL_RUNNING_LONG` explanation, and does not suppress `NO_PROGRESS`
(A2.4 items 6/7).

The block is absent before submission and on terminal executions, process-local, and never changes
`state`, `ended_at`, `unresolved`, `error`, the guard or idempotency. A client that does not know the
fields is unaffected.

### A2.4 Precedence and exact definitions

A pending native permission suppresses the `stall_reason` and its explanation. The `stall_reason`
list below is ordered and the first match wins **within that list**; `degradation_reason` (Variant A)
and the `explanation` are **independent dimensions** with their own precedence, so a wedged retry can
be both `stall_reason=NO_PROGRESS` and `degradation_reason=PROVIDER_RETRYING`, and a `busy`
`NO_PROGRESS` can carry `explanation=TOOL_RUNNING_LONG`.

1. **`HARNESS_UNREACHABLE`** — **only** when the probe's observation ends at a **counting native-read
   failure** (a transport/connection error, a read timeout, or a 5xx from the native client; the probe
   stops at its first failing read), consecutive probes over `unavailable_s` (default 120 s) each end
   at such a failure, and the execution is not terminal. The discriminator is the probe's **failure
   class**, never the shared `failed()` helper (`OBSERVATION_UNAVAILABLE` alone is shared by the
   native reads, the workspace checks and cancellation). **S3 must thread the error out of the current
   `failed(…, nil)` calls at `reconcile.go:244,258,262,266`** and check `ctx.Err()`/`m.ctx.Err()`
   before classifying a read failure:
   - **counts:** class `native_unavailable` — a native metadata/status/history/permissions read
     failed with a transport/connection error, a read timeout (including HTTP 408), or a 5xx. The
     probe's first failing read is the observation; "every native read failed" is not a reachable
     state.
   - **never counts:** class `native_refused` — a native 401/403/404 or any other 4xx (429 etc.), a
     3xx, a body that does not decode (`client.go:205`), or an over-large response (reachable but
     unusable); class `local_fs` (the workspace identity-check `errFSTimeout` at `:249,274,299` — a
     local filesystem stall, not the harness); class `canceled` (caller cancellation/expiry; check it
     **before** classifying the read error); class `identity` (`IDENTITY_MISMATCH`, any call site,
     including the cwd mismatch at `reconcile.go:340-344`); and an unrecognized native status type (it
     keeps the A1 liveness with no reason).
   `unavailable_since` is kept on the run and cleared by the first successful probe or by any
   non-counting failure; a non-counting failure is its own state, never `HARNESS_UNREACHABLE`. The
   supervisor should check the native client, the harness process and the transport; for a
   401/403/404 it checks the credential/session instead (a different failure). `cause_hint=unknown`.
2. **`COMPLETED_WITHOUT_TERMINAL_EVIDENCE`** — A1 definition unchanged.
3. **`NO_CORRELATED_ASSISTANT`** — A1 definition unchanged.
4. **`PROVIDER_ERROR`** (degradation in Variant A) — the execution is unresolved, `native_status` is
   not `retry`, and a typed provider error is available (an assistant error without
   `time.completed`+terminal proof, or a `retry` part if present; a terminal error is already
   `failed` and clears liveness). This is a **narrow** window (R3a persisted no error), not a common
   case. `cause_hint` from the typed error.
5. **`PROVIDER_RETRYING`** (degradation in Variant A) — `native_status=retry` (OpenCode) or the
   Claude retry state is active, **and the retry is not the wedged case of item 7**. A **healthy**
   retry (a future `next`) is only `PROVIDER_RETRYING`: in Variant A it never sets `stall_suspected`.
   An advanced attempt normally sets a future `next`; if the new `next` is already past with no
   progress, item 7 applies. `retry_attempt`/`retry_next_at`/`retry_max` accompany it. **A wedged
   retry — `next` past, no progress for more than `no_progress_s`, including a running part — is item
   7 `NO_PROGRESS`**, exactly A1's rule, with `degradation_reason=PROVIDER_RETRYING` as the additional
   dimension; `retry_overdue` **may or may not** accompany it (it needs the grace/attempt window; the
   `NO_PROGRESS` does not), and the `tool`/`TOOL_RUNNING_LONG` explanation is additional. **When
   `retry_overdue` is true the degradation reason is `PROVIDER_RETRY_OVERDUE` (D32 Q11); otherwise it
   is `PROVIDER_RETRYING`.** Variant A
   and Variant B both keep `stall_suspected=true` for it. A changed attempt resets
   `retry_attempt_since` and clears `retry_overdue`; it does not by itself clear the retry state.
6. **`TOOL_RUNNING_LONG` (explanation, not a reason)** — `native_status=busy` (or a wedged retry with
   a live running part) and the newest correlated assistant has a `tool` part with
   `state.status=running`, `now - state.time.start ≥ tool_long_s` (default 600 s), and no later
   completed/error part for the same call. It sets `explanation=TOOL_RUNNING_LONG` and the `tool`
   object; it **never** suppresses or replaces A1's `NO_PROGRESS` (item 7). With `native_status=absent`
   (for example after a harness restart) the stale running part is **never** a running tool and never
   `TOOL_RUNNING_LONG`; A1's rules apply.
7. **`NO_PROGRESS`** — A1's definition, kept: `native_status=busy` with no native progress (A1
   `last_native_progress_at` or submission) for **more than** `no_progress_s` (default 120 s), **even
   with a running tool**; **or** a **wedged retry** (`native_status=retry` on OpenCode, or the Claude
   retry state active with `retry_next_at` past, and no progress for more than `no_progress_s`, even
   with a running part) — A1's rule, kept unchanged. The baseline is A1's `max(started,
   last_native_progress_at)`; that is intentional and not changed. A healthy retry (future `next`) is
   item 5, not here. The wedged retry carries the `PROVIDER_RETRYING` degradation and may carry
   `retry_overdue` as its explanation; a `busy` `NO_PROGRESS` with a running tool carries the
   `tool`/`TOOL_RUNNING_LONG` explanation.

**Q1 (decided by D32: Variant A): `stall_suspected` vs a routine retry.** A1 says a retry with `next`
is positive evidence, test 3 "Retry is not a stall", and CONTRACTS says `stall_suspected` is true only
with a `stall_reason`. **D32 chose Variant A.** The variants are kept for traceability; A2 implements
Variant A:

- **Variant A (chosen by D32; no A1 text change for the A1 reasons):**
  `PROVIDER_RETRYING`/`PROVIDER_ERROR` are reported in `degradation_reason` (the `degraded` alias);
  `stall_reason` and `stall_suspected` keep A1's meaning **for the A1 reasons**, including the wedged
  retry and a `busy` execution with a running tool (`NO_PROGRESS`/`stall_suspected`); a **healthy**
  retry never sets `stall_suspected`. The boolean **additionally** becomes true for the one new A2
  stall reason `HARNESS_UNREACHABLE` (A1 gave `unavailable` no reason); the `TOOL_RUNNING_LONG` is an
  explanation of an existing A1 `NO_PROGRESS`, not a new boolean case. CONTRACTS
  (`docs/CONTRACTS.md:331-332`) must add `HARNESS_UNREACHABLE` to the `stall_reason` set and keep the
  wedged-retry/`NO_PROGRESS` rows; the `TOOL_RUNNING_LONG` explanation is not a `stall_reason`.
- **Variant B (not chosen; kept for the record):** the provider reasons move into `stall_reason` and
  `stall_suspected` becomes true for them, including a healthy retry; A1 test 3 and the CONTRACTS
  sentence would be **superseded** by that decision. The wedged retry remains a stall either way.

Q1 is decided by D32 (Variant A); S1–S5 implement it.

### A2.5 `cause_hint` mapping (structured fields, plus the D32 Q5 bounded fallback)

Structured sources, newest applicable first; one closed word, `cause_source` records provenance. **No
raw text is ever exported**; `message`, `action.title`, `responseBody`, tool names and provider names
never enter the block. The D32 Q5 fallback only maps a closed pattern to a closed word and sets
`cause_source=bounded_text`; the matched text is never authority, never exported, never in audit.

| Source (OpenCode) | Mapping |
| --- | --- |
| `status.retry.action.reason` | `free_tier_limit`, `account_rate_limit` → `rate_limit`; anything else (open string) → `unknown` (still `cause_source=structured`) |
| `assistant.error.name == ProviderAuthError` | `auth` |
| `assistant.error.name == APIError` (or a `retry` part's `error`, same `{name,data}` shape) | `data.statusCode` 401/403 → `auth`; 429 → `rate_limit`; 408/500/502/503/504/529 → `server`; absent/0 → `network`; any other → `unknown` |
| `assistant.error.name` in `ContextOverflowError`, `ContentFilterError`, `MessageOutputLengthError`, `StructuredOutputError`, `MessageAbortedError`, `UnknownError` | `unknown` (not a provider transport cause) |

| Source (Claude `api_retry`) | Mapping |
| --- | --- |
| `error_status` non-null | 401/403 → `auth`; 429 → `rate_limit`; 408/5xx → `server`; else `unknown` |
| `error_status == null` or `no_response` present | `network` (a documented no-response is a structured connection-level signal) |
| `error` enum with no status | `rate_limit`→`rate_limit`; `authentication_failed`→`auth`; `server_error`/`overloaded`→`server`; `billing_error`/`invalid_request`/`max_output_tokens`/`oauth_org_not_allowed`/`account_on_hold`/`model_not_found`/`cloud_credential_error`/anything else → `unknown` |

**Option 2 (bounded text, accepted by D32 as an addition to Option 1).** Lowercase at most 4 KiB of
the retry message, match the **closed** pattern set (429/rate limit, 5xx/overloaded, DNS/TLS/
connection, 401/403, timeout), set `cause_source=bounded_text` without exporting the text. The D32 Q5
decision accepts this fallback as part of the design: it is **bounded, redacted, closed-set,
report-only**; the matched text is **never authority, never exported and never in audit**; it adds no
input and no native read. It lands in **S1** (the OpenCode structured retry/error slice), which
already reads the retry message. The pattern set and limits are exactly A2.5's; widening them needs a
new decision.

### A2.6 Thresholds (operator-configured; `unavailable_s` fixed) and the deferred per-execution input

| Threshold | Default | Bounds | Where |
| --- | --- | --- | --- |
| `no_progress_s` | 120 s (A1) | 30–900 s | operator config/flag |
| `tool_long_s` | 600 s global; **D32 Q10 per-tool defaults** (for example Bash longer than Read) | 60–3600 s; **D32 Q12 caps it at the Claude `RUN_MAX`** when the Claude adapter is on. At `RUN_MAX` the Bridge **interrupts** the child (SIGINT then SIGTERM, never SIGKILL; a child that ignores both is abandoned) and latches `unknown`/`MONITOR_EXPIRED` with the guard kept (ADR-021 row 9), so a larger tool threshold can never fire | operator config/flag |
| `unavailable_s` | 120 s | fixed in the first slice | fixed in the first slice |

**Decided configuration names (D32 Q13).** The names follow ADR-026 §2 (`CIRAK_*`, PR #468) and
ADR-027 (PR #466): `CIRAK_LIVENESS_NO_PROGRESS_S` and `CIRAK_LIVENESS_TOOL_LONG_S` (environment), and
`liveness.no_progress_s`/`liveness.tool_long_s` in the ADR-027 file. Each is an integer number of
seconds; a non-integer, an out-of-range value, or an invalid combination is a **startup error**
naming the variable (like `BRIDGE_DASHBOARD_REDACT_EXTRA`/`CIRAK_DASHBOARD_LABEL`), never a silent
default; absent uses the default. `unavailable_s` stays fixed at 120 s. **Ordering dependency:**
ADR-026 S1 (the `CIRAK_*` rename) and ADR-027 are still proposed; if ADR-026 S1 lands before A2 S3,
the variables are introduced as `CIRAK_*` only; if A2 S3 lands first, they are introduced as
`BRIDGE_LIVENESS_*` and ADR-026 S1 renames them with its legacy window. The names above are the
target after ADR-026 S1; there is no pre-existing `BRIDGE_LIVENESS_*` twin to preserve.

Option 1 is **output-only**: it adds no input to `send_prompt` (A1 accepted output fields only), so
the accepted A1 statement stays true. **D32 Q3 keeps it operator-config only**; a per-execution
override is not accepted in v2. If the maintainer later wants it, the proposal is a new optional
input named `liveness_thresholds` (not `liveness`, which is the response object; CONTRACTS says input
schemas are unchanged today), and it must be **part of the ADR-010 idempotency fingerprint**: the same
key with different thresholds is `IDEMPOTENCY_CONFLICT`, and a replay returns the recorded thresholds
unchanged. That rule is stated here so a future change cannot be ambiguous; it is not accepted by
this amendment.

A threshold never changes `state`, the guard or a native call, and never retroactively changes an
execution.

### A2.7 Claude Code mapping

No argv change (`--include-partial-messages` is not used; text/thinking deltas stay unavailable, so a
long model turn can only be `NO_PROGRESS`). S4 adds the `tool_use` id to the decoder's block so
parallel tools pair with their `tool_result`.

| Event / state | Liveness |
| --- | --- |
| Child admitted, before `init` | no block |
| `system/init` seen | `native_status=busy`; `progress_source=bridge`; start |
| `system/api_retry` | `PROVIDER_RETRYING` (degradation in Variant A); `retry_attempt`, `retry_max`, `retry_next_at=now+retry_delay_ms`; `cause_hint` per A2.5 (including `no_response`); a later event clears the retry state; `retry_overdue` follows A2.3; a retry whose `retry_next_at` is past with no progress beyond `no_progress_s` (even with a running part) is `NO_PROGRESS`/`stall_suspected` (A2.4 item 7); `retry_overdue` may or may not accompany it |
| `assistant` with a `tool_use` block | running tool; `tool_name`, `running_since` = Bridge receive time; the `tool` object; A1's `NO_PROGRESS` at `no_progress_s` is not suppressed; at `tool_long_s` the `TOOL_RUNNING_LONG` explanation (while the child runs) |
| `user` with the matching `tool_result` | tool cleared; progress updated |
| `result` / terminal verdict (existing rows) | terminal; liveness cleared by A1's rule |
| after the cap latch (ADR-021 row 9) or the exit-grace latch (row 6a) | the execution is `unknown`, not terminal; the liveness block is cleared (or shows the latch reason), never `busy`/`TOOL_RUNNING_LONG` for the interrupted/abandoned child |
| the reader/stream fails (bounded out, broken stream) | `native_status=unavailable`; `HARNESS_UNREACHABLE` only from the counting class of A2.4 item 1 after `unavailable_s` (`unavailable_since` on the run); **Claude cases that count: a stdout read error while the child is alive (bounded); never an ADR-021 terminal row (cap, exit grace, aborted, exit) or an `Undecodable` line**; the child/verdict logic is untouched. An `api_retry` is **not** a read failure |
| no native status endpoint | `busy` while the child runs; `absent` is never emitted for Claude |

`progress_source=bridge` makes the receive-clock provenance explicit. Liveness is served on
`get_execution_status`, `get_execution_result` and the replay response, as for OpenCode, and is
process-local.

### A2.8 Dashboard display

Execution rows/detail gain a read-only badge: `retrying` (attempt/max, next; `overdue` and `PROVIDER_RETRY_OVERDUE` when
`retry_overdue`), `provider error` (cause), `tool running` (the `TOOL_RUNNING_LONG` explanation and
the name/elapsed), `stalled` (`NO_PROGRESS`), `unreachable` (since). The detail shows the reason
fields, `cause_hint`, `cause_source`, retry/tool fields and `observed_at`; the poll JSON gains the
same optional fields. No new route, write, token or free text; unknown renders `unknown`.
Archived-session and audit behavior are unchanged. In Variant A a **healthy** retry is
`retrying`/degraded, not `stalled`; a **wedged** retry is both `retrying`/degraded and `stalled` (it
keeps the A1 `NO_PROGRESS`); a `busy` `NO_PROGRESS` with a running tool stays `stalled` and shows the
`tool running` explanation; `HARNESS_UNREACHABLE` is `unreachable`.

### A2.9 Supervisor guidance (unchanged safety rules)

| Signal | Wait | Interrupt | Follow-up |
| --- | --- | --- | --- |
| `PROVIDER_RETRYING` | until `retry_next_at` + one probe; if `retry_overdue` is true (A2.3) the retry is a possible wedge; if the execution is also `NO_PROGRESS` (it may or may not carry `retry_overdue`), use the A1 `NO_PROGRESS` row; no separate two-probe rule is needed | explicit decision on a suspected wedge (never automatic) | only after terminal |
| `PROVIDER_ERROR` | read status/result/history; a terminal verdict wins | no | only after terminal; unknown → fenced recovery |
| `NO_PROGRESS` + `TOOL_RUNNING_LONG` explanation | yes for a known build (raise the operator threshold, respecting `RUN_MAX`); the explanation is not a separate suppression | explicit decision if it persists | only after terminal |
| `NO_PROGRESS` | one more probe interval; a known long build shows the explanation | if it persists | no |
| `HARNESS_UNREACHABLE` | retry the poll; check the Bridge's native client, the harness process and the transport; a 401/403/404 (`native_refused`) is not this signal (check the credential/session instead) | no (the Bridge cannot signal a child it cannot see) | no |
| `COMPLETED_WITHOUT_TERMINAL_EVIDENCE` | no | no | ADR-010 fenced recovery; never a new key while the guard is held |

Never resubmit an uncertain prompt, never reuse a key, never infer success, never release the guard
from liveness; the maximum five autonomous corrective iterations is unchanged; an interrupt stays an
explicit supervisor action.

### A2.10 What stays impossible

- Chat cannot be woken; it only sees this on poll.
- No native signal proves death; `HARNESS_UNREACHABLE` is an observation fact about the Bridge's own
  reads, not the provider, and not a death certificate. A native 401/403/404 (`native_refused`), a
  workspace `errFSTimeout` (`local_fs`) and caller cancellation (`canceled`) are **not** this signal
  (A2.4 item 1).
- **OpenCode network vs provider** for a status-only retry is now covered by the D32 Q5 bounded
  fallback (a closed pattern gives a hint, `cause_source=bounded_text`), not by a guess; a text that
  matches no pattern stays `unknown`. Q7 (retry-part persistence) remains UNVERIFIED and is no longer
  required for that attribution.
- Hung tool vs legitimately long tool, and thinking vs a hung model call, are intent/policy questions;
  A1's `NO_PROGRESS` is kept even with a running tool, and the `TOOL_RUNNING_LONG` explanation lets a
  human distinguish a known build; a stale running tool part with status absent is not reported as
  "running" (it is not).
- A Bridge restart loses liveness with the execution; old keys stay dead (ADR-010).
- Native clocks can skew; A1's future-timestamp rule stays.
- The total OpenCode retry budget is not exposed (the pinned constant is 5; configuration-dependent
  behavior is UNVERIFIED). A retry that never advances is A1's wedged-retry
  `NO_PROGRESS`/`stall_suspected` (kept under Variant A, A2.4 item 7) plus, **when its own window is
  met**, `retry_overdue`; the `retry_overdue` grace is advisory, but the A1 stall signal is not lost.
- The Claude `api_retry` field set and the 2.1.325 behavior are UNVERIFIED; the parser is lenient and
  maps unknown enum values to `unknown`.

### A2.11 Accepted design (D32) and the record of the options

**The accepted design is Option 1 plus the D32 Q5 bounded-text fallback.** The structured output-only
base (Option 1) is accepted with the A2.3 fields, A2.4 precedence (Variant A), A2.5 structured
mapping **and the bounded fallback**, A2.6 operator thresholds, A2.7 Claude mapping, A2.8 dashboard.
The base adds no input to `send_prompt`; the fallback adds no input and no native read (it reuses the
retry message already on the status stream). It is bounded (4 KiB), redacted, closed-set,
report-only; the matched text is never authority, never exported, never in audit; it lands in S1.

- **Option 1 (the structured base; accepted):** the A2.3 fields, A2.4 precedence (Variant A), A2.5
  structured mapping, A2.6 operator thresholds, A2.7 Claude mapping, A2.8 dashboard. No new input on
  `send_prompt`; no new native read.
- **Option 2 (the bounded-text fallback; accepted by D32 Q5):** the A2.5 fallback on top of the
  structured base; it closes the OpenCode network gap for a status-only retry. Bounded, redacted,
  closed-set, report-only; S1.
- **Option 3 (consume `/event`):** deferred; delivery/ordering/durability are UNVERIFIED (A1 N12);
  later it can feed the same fields.

The D32 design is accepted; the slices implement it. Q1 is Variant A, Q3 keeps operator-config only,
Q5 is the Option 2 fallback, Q13 the names; Q7 stays factual/UNVERIFIED and is no longer a blocker
because the fallback supplies the network attribution. A1's report-only contract and ADR-010/011 are
untouched.

### A2.12 Test plan (RED-first, no live services)

Fabricated streams/status/history; no live CLI, provider or host. Each case gets a RED test first and a
mutation below.

1. Retry with `action.reason=account_rate_limit` → `PROVIDER_RETRYING` (or `degradation_reason`),
   `cause_hint=rate_limit`, attempt/next recorded.
2. Retry with an unknown `action.reason` → the structured source is `unknown`; the D32 bounded
   fallback may refine it from the message (`cause_source=bounded_text` when it does, `structured`
   otherwise); the raw text is never exported.
3. Retry network (no `action`, no structured error), message matching the closed DNS/TLS/connection
   pattern → `cause_hint=network`, `cause_source=bounded_text`; a message matching no pattern →
   `unknown`; the raw text is never exported; the ADR asserts the fallback, not a guess.
4. `busy` + running tool 1 s and 119 s → no `NO_PROGRESS`; 121 s → **`NO_PROGRESS` and
   `stall_suspected=true` (A1's rule, a boolean-only client stays true)**, the `tool` object; 600 s
   (boundary) → the same `NO_PROGRESS` plus `explanation=TOOL_RUNNING_LONG`; `absent` status + a
   stale running part → **no** `TOOL_RUNNING_LONG`, no running tool, A1's rules; a wedged retry with
   a **stale** running part → `NO_PROGRESS` (A1's retry rule) with no tool explanation; a wedged retry
   with a **live** running part → `NO_PROGRESS` plus the `tool` object.
5. `busy`, no tool, 119 s → no reason; 121 s → `NO_PROGRESS` (the `>` boundary).
6. Completed assistant without finish/error (A1 shape) → `COMPLETED_WITHOUT_TERMINAL_EVIDENCE`
   (unchanged), state/guard unchanged.
7. Assistant `ProviderAuthError` without terminal proof → `PROVIDER_ERROR`, `cause_hint=auth`.
8. Typed APIError statuses 401/403/429/500/503/529/absent → auth/rate_limit/server/network as A2.5.
9. Table-driven over the probe failure classes: a native metadata/status/history/permissions
   transport error, read timeout (including 408) or 5xx on each consecutive probe for 120 s → **yes**,
   `HARNESS_UNREACHABLE` with `unavailable_since` on the run; a native 401/403/404, 429, other 4xx,
   a 3xx, an undecodable body or an over-large response → **no** (reachable but unusable);
   `IDENTITY_MISMATCH` (metadata/session/workspace, including the cwd mismatch) → **no**; a workspace
   identity-check `errFSTimeout` (`reconcile.go:249,274,299`) → **no** (local filesystem); a read
   that fails with `ctx.Err()`/`m.ctx.Err()` set → **no** (check before classifying); an unrecognized
   status type → **no**; a successful probe or a non-counting failure clears `unavailable_since`.
   The discriminator is the **failure class** (`native_unavailable` counts;
   `native_refused`/`local_fs`/`canceled`/`identity` never), not the shared `failed()` helper;
   "every native read of this probe failed" is not a reachable state — the probe's first failing read
   is the observation. Also assert that a hung local filesystem check never produces
   `HARNESS_UNREACHABLE`.
10. A pending permission suppresses every reason.
11. Retry with `attempt=2`, `next` past by > `max(30 s, no_progress_s)` and `retry_attempt_since`
    61 s old (`no_progress_s` 60 s in the fixture) → `retry_overdue=true`; an advanced attempt (which
    resets `retry_attempt_since`) or a future `next` clears it; **a wedged retry (next past, no
    progress > `no_progress_s`, a running part allowed) is `stall_reason=NO_PROGRESS` and
    `stall_suspected=true` under Variant A**, with `degradation_reason=PROVIDER_RETRYING` (or
    `PROVIDER_RETRY_OVERDUE` when `retry_overdue` is true); the
    wedged retry **may or may not** carry `retry_overdue` — add a second fixture with no progress > `no_progress_s`, `next` past only 5 s and
    `retry_attempt_since` 10 s to pin `NO_PROGRESS` without `retry_overdue`; a healthy
    retry (future `next`) is `PROVIDER_RETRYING` and, under Variant A, not a stall.
12. Claude `system/api_retry` → `PROVIDER_RETRYING` with attempt/max/next and the mapped hint; a
    `no_response` object → `network`; `overloaded` → `server`; a later event clears the retry state; a
    retry whose `retry_next_at` passes with no progress beyond `no_progress_s` (even with a running
    part) is `NO_PROGRESS`/`stall_suspected` (A2.4 item 7), with `retry_overdue` only when its own
    window is met.
13. Claude `tool_use` without `tool_result` → running tool; the `tool` object; A1's `NO_PROGRESS` at
    `no_progress_s` is not suppressed; `tool_result` clears it; over `tool_long_s` while the child
    runs → the `TOOL_RUNNING_LONG` explanation; a **live-child stdout read error** (bounded) is the
    only Claude source of `HARNESS_UNREACHABLE`; an ADR-021 terminal row (cap, exit grace, aborted,
    exit) or an `Undecodable` line never counts; after a cap/exit-grace latch the liveness is cleared
    (or shows the latch reason), never `busy`/`TOOL_RUNNING_LONG`.
14. Malformed/unknown events, oversized parts/lines and hostile timestamps are ignored without panic;
    a timestamp after `observed_at` is ignored.
15. No native mutation: zero Prompt/Abort calls; state, guard, idempotency and replay unchanged; a
    different payload still conflicts.
16. JSON compatibility: old parsers decode the block; new fields optional and omitempty.
17. For Variant A: a **healthy** retry never sets `stall_suspected`; a **wedged** retry (next past, no
    progress > `no_progress_s`, a running part allowed) does, via `NO_PROGRESS` (A1's rule); a `busy`
    execution with a running tool does (A1's rule); `HARNESS_UNREACHABLE` also sets it (the one new
    true case). `TOOL_RUNNING_LONG` is an explanation and does **not** independently set it. Variant
    B is not chosen; the suite keeps the A/B parameterization for traceability.
18. If a future decision adds `liveness_thresholds`: same key + different thresholds →
    `IDEMPOTENCY_CONFLICT`; a replay returns the recorded thresholds. (Not in v2; D32 Q3 keeps
    operator-config only.)
19. `go test -race -count=20` on the touched packages with concurrent probes/polls.
20. Operator thresholds: an absent variable uses the default; a valid in-range integer is applied and
    echoed in `thresholds`; a non-integer, an out-of-range value or an invalid combination is a
    startup error naming the variable; the `CIRAK_LIVENESS_*` name and the ADR-027 key resolve to the
    same value; a per-tool override (D32 Q10, for example Bash) applies to that tool and the global
    default otherwise. **No legacy `BRIDGE_LIVENESS_*` twin exists or is accepted**; the names follow
    ADR-026 S1's ordering (A2.6).

Mutations (must be killed): a **healthy** retry reported as `NO_PROGRESS`; a **wedged** retry not
reported as `NO_PROGRESS`/`stall_suspected` under Variant A; a retry never reported; 429 mapped to
`server`; **a running tool suppresses `NO_PROGRESS`** (the P2 regression); a stale running part
reports `TOOL_RUNNING_LONG`; a bounded-fallback match copied into a field/audit or the pattern set
widened; identity mismatch, a workspace `errFSTimeout`, a 401/403/404, a 3xx/decode/over-size read,
or a cancelled read produces `HARNESS_UNREACHABLE`; `unavailable_since` lost per probe;
`retry_overdue` set without `retry_attempt_since` or implied by `NO_PROGRESS`; the free-text message
copied into a field; a stale retry reason not cleared; liveness touching state/guard; the Claude
`retry_max` not carried; `no_response` mapped to `unknown`; a cap/exit-grace latch still reported as
`busy`; the dashboard rendering a raw tool name untruncated; a replay flag or threshold fingerprint
wrong.

### A2.13 Slices (one PR each; the ADR is ACCEPTED per D32; S1 implements it)

1. **S1 — OpenCode structured retry/error + the D32 fallback:** parse status `message`/`action` and a
   `retry` part if present; `PROVIDER_RETRYING`/`PROVIDER_RETRY_OVERDUE`/`PROVIDER_ERROR` in Variant A's field;
   `cause_hint`/`cause_source`; the D32 Q5 bounded-text fallback (bounded, redacted, closed set,
   report-only, the matched text never exported/audit); `retry_overdue` with the
   `retry_attempt_since` memory; keep A1's wedged-retry `NO_PROGRESS` (with a running part);
   CONTRACTS update (`HARNESS_UNREACHABLE` only; no `TOOL_RUNNING_LONG` reason).
2. **S2 — running tool:** `tool` object, the `TOOL_RUNNING_LONG` explanation, **A1's `NO_PROGRESS` is
   not suppressed**, the absent/stale case, the wedged retry with a live/stale part.
3. **S3 — unreachable + operator thresholds:** the **native-read failure class** (A2.4 item 1: thread
   the error out of `failed(…, nil)`, classify decode/3xx/408/over-size, check `ctx.Err()` first;
   class `native_unavailable` only), `unavailable_since` on the run, the operator thresholds (A2.6
   names, D32 Q12/Q13); **not** a per-execution input (D32 Q3).
4. **S4 — Claude mapping:** decode `api_retry`/`no_response`, add the `tool_use` id, tool pairs,
   liveness for Claude executions (including the wedged retry on the receive clock, A2.4 item 7), the
   counting live-child reader failure only, the cap/exit-grace latch rule; no argv change.
5. **S5 — dashboard:** badges/detail and poll JSON; read-only; Variant A shows a healthy retry as
   `retrying`/degraded, a `busy` running-tool `NO_PROGRESS` as `stalled` plus the `tool running`
   explanation, and a wedged retry as both `retrying`/degraded and `stalled`.

Each slice is RED-first with the cases/mutations it owns; no live service is needed for acceptance.

### A2.14 Decided (D32) and the one factual question

The maintainer decided every decision question below in D32; the decisions are recorded here as the
accepted answers. Q7 is factual (UNVERIFIED live behaviour), not a decision.

1. **Q1 — decided: Variant A.** Separate `degradation_reason`/`degraded`; A1's
   `stall_reason`/`stall_suspected` for the A1 reasons, including the wedged retry and a `busy`
   execution with a running tool; the one new boolean-true case is `HARNESS_UNREACHABLE`; a healthy
   retry is not a stall. Variant B is not chosen.
2. **Q2 — decided: bounded 64-byte tool name**, sanitized, never in audit.
3. **Q3 — decided: operator-config only**; no `send_prompt` input in v2; a future
   `liveness_thresholds` would carry the idempotency-fingerprint rule.
4. **Q4 — decided: time-based** (`unavailable_s`), not N consecutive failed probes.
5. **Q5 — decided: Option 2**, the closed-pattern bounded free-text fallback (bounded 4 KiB,
   redacted, closed set, report-only; the matched text never authority/exported/audit); it is an
   addition to the structured Option 1 base and lands in S1.
6. **Q6 — decided: the lenient Claude parser** with `unknown`; a live 2.1.325 confirmation is not a
   prerequisite, and the field set stays UNVERIFIED.
7. **Q7 — not decided by D32 — factual, remains UNVERIFIED** (a live check of whether OpenCode
   persists a `retry` part while a retry is pending); with Q5 Option 2 accepted, network attribution
   no longer depends on it. It stays a limitation.
8. **Q8 — decided: newest wins** when `action.reason` and the typed error disagree.
9. **Q9 — decided: the proposed dashboard badges** (`retrying`, `overdue`, `provider error`, `tool
   running`, `stalled`, `unreachable`) and `unknown` instead of a guess.
10. **Q10 — decided: per-tool defaults** (for example Bash longer than Read), not one global
    `tool_long_s`.
11. **Q11 — decided: a separate `PROVIDER_RETRY_OVERDUE` reason** for an overdue retry.
12. **Q12 — decided: `tool_long_s` is capped at `RUN_MAX`** automatically when the Claude adapter is
    on.
13. **Q13 — decided: the names** `CIRAK_LIVENESS_NO_PROGRESS_S`/`CIRAK_LIVENESS_TOOL_LONG_S` and
    `liveness.no_progress_s`/`liveness.tool_long_s`, with A2.6's bounds, startup-error rule and the
    ADR-026 S1 ordering dependency.
