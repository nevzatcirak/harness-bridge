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

## Amendment A2 (PROPOSED, revised 2026-11-21 after reviews #458/#459): stall and degradation taxonomy v2

Status: **PROPOSED, not accepted.** This amendment answers the maintainer need of 2026-11-21: network
drops, a provider that fails or keeps retrying, and a hung or long-running child tool must be
distinguishable so the supervisor can wait, interrupt or follow up. It is additive to the accepted A1
(option (a)); until accepted the Bridge serves exactly A1. It preserves ADR-010/011: **report-only**;
no automatic Abort, resubmission, guard release, new key or inferred success; recovery from a
confirmed stall remains the ADR-010 fenced procedure. A2 does **not** take the maintainer decisions in
A2.14 Q1 (retry vs `stall_suspected`) or Q3 (per-execution thresholds); both are isolated below.

*Revision note (for the delta review): the first draft's claims about OpenCode network attribution,
the `send_prompt` override, `HARNESS_UNREACHABLE`, retry semantics vs A1 test 3, a stale running tool
and the dropped wedged-retry case were wrong or overclaimed; the affected sections now state the
limits and the open decisions, and the code-level citations were re-checked at `e2ac772`.*

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
| `native_status=unavailable` is set on any failed probe **and** on an identity mismatch (`IDENTITY_MISMATCH`); an unrecognized status type also maps to `unavailable` | `internal/execution/reconcile.go:229-235,340-344`; `internal/execution/liveness.go:139-140` | verified (code): a **dedicated** read-failure condition is needed for A2.4 item 1 |
| The probe already reads status, history and permissions; A2 adds no native read | `internal/execution/reconcile.go:216-345` | verified (code) |

**Claude Code 2.1.325.** The runner already spawns the CLI with `--output-format stream-json
--verbose` (`internal/adapters/claudecode/argv.go:160-164`), so `system/api_retry` is already on the
stream. `DecodeLine` parses `system/init`, `result`, and assistant `tool_use` / user `tool_result`
blocks (`internal/adapters/claudecode/decoder.go:180-225`); `Block` carries the tool name and input
but **no tool_use id**, so S4 must add the id to pair parallel tools (P3-1). The state machine ignores
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
| Network down (OpenCode, while a prompt runs) | status `retry` with a **free-text** message (no `action` for a network failure), or `busy`, or `unavailable` | A **status-only** retry is `PROVIDER_RETRYING`/`degradation_reason` with `cause_hint=unknown`; `network` requires a structured no-HTTP signal: an assistant `APIError` with no `statusCode`, or a persisted `retry` part (Q7, UNVERIFIED), or Claude `error_status=null`/`no_response` | **Option 1 does not distinguish network from provider for the common OpenCode status-only retry** until Q7 is answered; A2 says so instead of overclaiming |
| Provider retrying | `native_status=retry`, attempt/next; no reason | `PROVIDER_RETRYING` (degradation), attempt/next/max, `retry_overdue` when the retry is past due and the attempt is not advancing; `cause_hint` from a structured source only | The total retry budget is not exposed by OpenCode (the pinned constant is 5, but config-dependent, UNVERIFIED); the reason does not prove a wedge by itself |
| Provider hard error | terminal `failed` if the assistant carries a named error; otherwise `unknown`/`MONITOR_EXPIRED` (R3a) | Terminal unchanged; if no terminal proof, a **narrow** `PROVIDER_ERROR` from a typed error (assistant error without `time.completed`, or a `retry` part) with a cause | Whether an error without terminal evidence means the turn ended is UNVERIFIED (R3a N5/N8); R3a itself persisted **no** error |
| Hung tool child | `busy` + part timestamps; A1 `NO_PROGRESS` at 2 min (also for a legitimate long tool) | `TOOL_RUNNING_LONG{tool_name, running_since}` only when `native_status=busy` and the tool is ≥ `tool_long_s`; any running tool suppresses `NO_PROGRESS` | No native liveness inside a tool (no output timestamp); a stale running part after a harness restart (status absent) is **not** reported as "tool running" — it stays with A1's shape |
| Legitimately long tool (build) | same | the same signal, plus the operator-configured threshold; the tool name/elapsed time let a human decide | The Bridge must not guess intent; a long build still shows a degradation badge |
| Model thinking a long time | `busy` + no completed part; `NO_PROGRESS` at 2 min | `NO_PROGRESS` (no distinct signal) | Neither harness distinguishes thinking from a hung model/provider call |
| Bridge→harness connection lost | `native_status=unavailable` (also set on identity mismatch/unknown type) | `HARNESS_UNREACHABLE` only from a **dedicated native-read failure** (not identity, not unknown type), after the window, with `unavailable_since` stored on the run | The error surface does not name the local/remote cause; no retry proves the old turn ended |

No case gains an automatic action.

### A2.3 v2 fields (additive, optional, closed sets, report-only)

All A1 fields keep their names and meanings. New optional fields:

| Field | Type / values | Meaning |
| --- | --- | --- |
| `degradation_reason` | `PROVIDER_RETRYING`, `PROVIDER_ERROR` (Variant A) | A provider problem that is not a stall; `degraded` is true iff present. Variant B moves these to `stall_reason` — see Q1 |
| `degraded` | bool (Variant A) | true iff `degradation_reason` is present; never a terminal state |
| `stall_reason` | A1 values plus `TOOL_RUNNING_LONG`, `HARNESS_UNREACHABLE` (and `PROVIDER_*` in Variant B) | A stall reason; A1 `stall_suspected` semantics are unchanged in Variant A |
| `retry_overdue` | bool, optional | true when the current retry's `next` is past by more than `max(30 s, no_progress_s)` **and** the attempt number has not advanced for at least `no_progress_s`; a changed attempt or a future `next` clears it |
| `cause_hint` | `network`, `rate_limit`, `auth`, `server`, `unknown` | Coarse provider cause from structured fields only (A2.5); never free text |
| `cause_source` | `structured`, `none` (v2 Option 1); `bounded_text` only if Q5's Option 2 is accepted | Provenance; a structured field that maps to `unknown` is still `structured` |
| `retry_max` | integer, optional | Claude `max_retries`; absent for OpenCode (not exposed) |
| `tool` | object, optional | `{tool_name, running_since}` of the oldest running tool of the newest correlated assistant; **only when `native_status=busy`** |
| `tool.tool_name` | string ≤64 bytes, `[A-Za-z0-9._-]` or `other` | Native tool name, sanitized; untrusted display data, never authority |
| `tool.running_since` | RFC3339 (native `state.time.start`, never after `observed_at`) | When the running tool started |
| `unavailable_since` | RFC3339, optional | First observation of the current **native-read failure** window (Bridge clock), stored on the execution record, not in the per-probe block; cleared by a successful probe |
| `thresholds` | object | `{no_progress_s, tool_long_s}` actually applied (operator-configured in Option 1) |
| `progress_source` | `native`, `bridge` | OpenCode: `native`; Claude: `bridge` |

`tool.last_output_at` is **removed**: neither harness exposes a per-tool output timestamp (A1 N11 is
per-part `time.start/end`; a running tool has only `start`). A stale running part with status absent
is not a `TOOL_RUNNING_LONG` (A2.4 item 6).

The block is absent before submission and on terminal executions, process-local, and never changes
`state`, `ended_at`, `unresolved`, `error`, the guard or idempotency. A client that does not know the
fields is unaffected.

### A2.4 Precedence and exact definitions

A pending native permission suppresses **every** reason. Otherwise the first match wins:

1. **`HARNESS_UNREACHABLE`** — **only** when every native read of this probe failed with a transport/
   filesystem error (the `reconcile.go:229-235` failure path), the failure is continuous for
   `unavailable_s` (default 120 s), and the execution is not terminal. **An identity mismatch
   (`IDENTITY_MISMATCH`) or an unrecognized native status type never produces it** (they keep the A1
   liveness, `native_status=unavailable`, with no reason); see P2-3/P2-4. `unavailable_since` is kept
   on the run and cleared by the first successful probe. `cause_hint=unknown`.
2. **`COMPLETED_WITHOUT_TERMINAL_EVIDENCE`** — A1 definition unchanged.
3. **`NO_CORRELATED_ASSISTANT`** — A1 definition unchanged.
4. **`PROVIDER_ERROR`** (degradation in Variant A) — the execution is unresolved, `native_status` is
   not `retry`, and a typed provider error is available (an assistant error without
   `time.completed`+terminal proof, or a `retry` part if present; a terminal error is already
   `failed` and clears liveness). This is a **narrow** window (R3a persisted no error), not a common
   case. `cause_hint` from the typed error.
5. **`PROVIDER_RETRYING`** (degradation in Variant A) — `native_status=retry` (OpenCode) or the
   Claude retry state is active. `retry_attempt`/`retry_next_at`/`retry_max` accompany it; set
   `retry_overdue` as in A2.3. **A1's "retry whose `next` is past" `NO_PROGRESS` is carried by
   `retry_overdue` rather than silently dropped**: an overdue retry with an unchanged attempt is a
   possible wedge and A2.9 tells the supervisor to act on it. A changed attempt clears it.
6. **`TOOL_RUNNING_LONG`** — `native_status=busy` **and** the newest correlated assistant has a
   `tool` part with `state.status=running`, `now - state.time.start ≥ tool_long_s` (default 600 s),
   and no later completed/error part for the same call. With `native_status=absent` (for example
   after a harness restart) the stale running part is **never** reported as `TOOL_RUNNING_LONG`;
   A1's rules apply (no reason unless another A1 shape matches). **Any running tool — even younger
   than `tool_long_s` — suppresses `NO_PROGRESS`**, because its `start` is the only progress signal
   and inventing `NO_PROGRESS` at 2 min would be a false stall; the 200 s case is a test, not a
   reason.
7. **`NO_PROGRESS`** — `native_status=busy` with no native progress (A1 `last_native_progress_at` or
   submission) for **more than** `no_progress_s` (default 120 s), and no running tool. Retry status
   is handled by item 5, not here.

**Q1 (blocking, not decided): `stall_suspected` vs a routine retry.** A1 says a retry with `next` is
positive evidence, test 3 "Retry is not a stall", and CONTRACTS says `stall_suspected` is true only
with a `stall_reason`. Two variants are specified and **A2 does not choose**:

- **Variant A (non-breaking, no A1 text change):** `PROVIDER_RETRYING`/`PROVIDER_ERROR` are reported
  in `degradation_reason`/`degraded`; `stall_reason` and `stall_suspected` keep A1's meaning, so a
  routine retry never sets `stall_suspected`. A client that reads the boolean alone keeps its A1
  behaviour.
- **Variant B (single field, needs the maintainer):** the provider reasons move into `stall_reason`
  and `stall_suspected` becomes true for them; A1 test 3 and the CONTRACTS sentence are **superseded**
  by that decision. This must not be presented as taken.

S1–S5 cannot implement either variant until Q1 is answered; Q1 is recorded as a blocker for
acceptance, not resolved by this amendment.

### A2.5 `cause_hint` mapping (recommended: structured fields only)

Structured sources, newest applicable first; one closed word, `cause_source` records provenance. **No
text is ever exported**; `message`, `action.title`, `responseBody`, tool names and provider names never
enter the block.

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

**Option 2 (bounded text, not recommended now).** Lowercase at most 4 KiB of the retry message, match a
closed pattern set (429/rate limit, 5xx/overloaded, DNS/TLS/connection, 401/403, timeout), set
`cause_source=bounded_text` without exporting the text. The earlier justification ("buys little over
unknown") was wrong for OpenCode status-only retries, where it is the only network signal. It is
deferred until Q5; if accepted it is its own change, because it duplicates the upstream policy and is
version-brittle.

### A2.6 Thresholds (operator-configured in Option 1) and the deferred per-execution input

| Threshold | Default | Bounds (proposed) | Where |
| --- | --- | --- | --- |
| `no_progress_s` | 120 s (A1) | 30–900 s | operator config/flag (Option 1) |
| `tool_long_s` | 600 s | 60–3600 s, and ≤ the Claude `RUN_MAX` when the Claude adapter is on (a child is killed at `RUN_MAX`, default 15 m; a larger tool threshold can never fire) | operator config/flag |
| `unavailable_s` | 120 s | fixed in the first slice | operator config |

Option 1 is **output-only**: it adds no input to `send_prompt` (A1 accepted output fields only), so the
accepted A1 statement stays true. A **per-execution override is a separate decision (Q3)**, not part
of Option 1 or slices S1–S5. If the maintainer later wants it, the proposal is a new optional input
named `liveness_thresholds` (not `liveness`, which is the response object; CONTRACTS says input
schemas are unchanged today), and it must be **part of the ADR-010 idempotency fingerprint**: the same
key with different thresholds is `IDEMPOTENCY_CONFLICT`, and a replay returns the recorded thresholds
unchanged. That rule is stated here so the future change cannot be ambiguous; it is not accepted by
this amendment.

A threshold never changes `state`, the guard or a native call, and never retroactively changes an
execution.

### A2.7 Claude Code mapping

No argv change (`--include-partial-messages` is not used; text/thinking deltas stay unavailable, so a
long model turn can only be `NO_PROGRESS`). S4 adds the `tool_use` id to the decoder's block so
parallel tools pair with their `tool_result` (P3-1).

| Event / state | Liveness |
| --- | --- |
| Child admitted, before `init` | no block |
| `system/init` seen | `native_status=busy`; `progress_source=bridge`; start |
| `system/api_retry` | `PROVIDER_RETRYING` (degradation in Variant A); `retry_attempt`, `retry_max`, `retry_next_at=now+retry_delay_ms`; `cause_hint` per A2.5 (including `no_response`); a later event clears the retry state; `retry_overdue` follows A2.3 |
| `assistant` with a `tool_use` block | running tool; `tool_name`, `running_since` = Bridge receive time; `TOOL_RUNNING_LONG` after `tool_long_s` only while the child runs (`native_status=busy`) |
| `user` with the matching `tool_result` | tool cleared; progress updated |
| `result` / terminal verdict (existing rows) | terminal; liveness cleared by A1's rule |
| the reader/stream fails (bounded out, broken stream) | `native_status=unavailable`; `HARNESS_UNREACHABLE` only from that read-failure condition after `unavailable_s` (`unavailable_since` on the run); the child/verdict logic is untouched. An `api_retry` is **not** a read failure |
| no native status endpoint | `busy` while the child runs; `absent` is never emitted for Claude |

`progress_source=bridge` makes the receive-clock provenance explicit. Liveness is served on
`get_execution_status`, `get_execution_result` and the replay response, as for OpenCode, and is
process-local.

### A2.8 Dashboard display

Execution rows/detail gain a read-only badge: `retrying` (attempt/max, next; `overdue` when
`retry_overdue`), `provider error` (cause), `tool running` (name, elapsed), `stalled` (`NO_PROGRESS`,
`TOOL_RUNNING_LONG`), `unreachable` (since). The detail shows the reason fields, `cause_hint`,
`cause_source`, retry/tool fields and `observed_at`; the poll JSON gains the same optional fields. No
new route, write, token or free text; unknown renders `unknown`. Archived-session and audit behavior
are unchanged. In Variant A the badge separates `retrying`/`degraded` from `stalled`, so a routine
retry does not read as a stall.

### A2.9 Supervisor guidance (unchanged safety rules)

| Signal | Wait | Interrupt | Follow-up |
| --- | --- | --- | --- |
| `PROVIDER_RETRYING` | until `retry_next_at` + one probe; if `retry_overdue` and the attempt is unchanged across two probes, treat it as a possible wedge and choose explicitly | explicit decision on a suspected wedge (never automatic) | only after terminal |
| `PROVIDER_ERROR` | read status/result/history; a terminal verdict wins | no | only after terminal; unknown → fenced recovery |
| `TOOL_RUNNING_LONG` | yes for a known build (raise the operator threshold, respecting `RUN_MAX`) | explicit decision if it persists | only after terminal |
| `NO_PROGRESS` | one more probe interval | if it persists | no |
| `HARNESS_UNREACHABLE` | retry the poll; check the Bridge's native client, the harness process and the transport | no (the Bridge cannot signal a child it cannot see) | no |
| `COMPLETED_WITHOUT_TERMINAL_EVIDENCE` | no | no | ADR-010 fenced recovery; never a new key while the guard is held |

Never resubmit an uncertain prompt, never reuse a key, never infer success, never release the guard
from liveness; the maximum five autonomous corrective iterations is unchanged; an interrupt stays an
explicit supervisor action.

### A2.10 What stays impossible

- Chat cannot be woken; it only sees this on poll.
- No native signal proves death; `HARNESS_UNREACHABLE` is an observation fact about the Bridge's own
  reads, not the provider, and not a death certificate.
- **OpenCode network vs provider** cannot be told from a status-only retry: the status `message` is
  free text and `action` exists only for the two usage-limit reasons; without a structured no-HTTP
  error (assistant `APIError` without `statusCode`, a persisted `retry` part — Q7 — or Claude
  `error_status=null`/`no_response`) the hint is `unknown`.
- Hung tool vs legitimately long tool, and thinking vs a hung model call, are intent/policy questions;
  a stale running tool part with status absent is not reported as "running" (it is not).
- A Bridge restart loses liveness with the execution; old keys stay dead (ADR-010).
- Native clocks can skew; A1's future-timestamp rule stays.
- The total OpenCode retry budget is not exposed (the pinned constant is 5; configuration-dependent
  behavior is UNVERIFIED). A retry that never advances is detected only by `retry_overdue` + the
  attempt-unchanged rule, which is advisory.
- The Claude `api_retry` field set and the 2.1.325 behavior are UNVERIFIED; the parser is lenient and
  maps unknown enum values to `unknown`.

### A2.11 Options and recommendation

- **Option 1 (recommended, output-only):** the A2.3 fields, A2.4 precedence (both Q1 variants
  specified), A2.5 structured mapping, A2.6 operator thresholds, A2.7 Claude mapping, A2.8 dashboard.
  No free-text parsing; no new input on `send_prompt`; no new native read.
  **Conditional:** it does **not** claim network-vs-provider for an OpenCode status-only retry until
  Q7 is answered (a live check of retry-part persistence) or Option 2 is accepted. The maintainer
  should not treat "tells network from provider" as delivered for OpenCode before that.
- **Option 2: structured + bounded-text fallback** (A2.5). Not recommended now; deferred to Q5, and
  specifically the path that could close the OpenCode network gap.
- **Option 3: consume `/event`** for push progress. Deferred: delivery/ordering/durability are
  UNVERIFIED (A1 N12); later it can feed the same fields.

Accept Option 1 in slices after Q1 (and Q3, if the override is wanted); keep A1's report-only contract
and ADR-010/011 untouched.

### A2.12 Test plan (RED-first, no live services)

Fabricated streams/status/history; no live CLI, provider or host. Each case gets a RED test first and a
mutation below.

1. Retry with `action.reason=account_rate_limit` → `PROVIDER_RETRYING` (or `degradation_reason`),
   `cause_hint=rate_limit`, attempt/next recorded.
2. Retry with an unknown `action.reason` → `cause_hint=unknown`, `cause_source=structured`, no text.
3. Retry network (no `action`, no structured error) → `cause_hint=unknown`; the ADR's limitation is
   asserted, not a `network` guess.
4. `busy` + running tool 1 s and 200 s → no `NO_PROGRESS`, no `TOOL_RUNNING_LONG`; 600 s (boundary) →
   `TOOL_RUNNING_LONG`; `absent` status + a stale running part → **no** `TOOL_RUNNING_LONG` and no
   reason (A1 behavior).
5. `busy`, no tool, 119 s → no reason; 121 s → `NO_PROGRESS` (the `>` boundary).
6. Completed assistant without finish/error (A1 shape) → `COMPLETED_WITHOUT_TERMINAL_EVIDENCE`
   (unchanged), state/guard unchanged.
7. Assistant `ProviderAuthError` without terminal proof → `PROVIDER_ERROR`, `cause_hint=auth`.
8. Typed APIError statuses 401/403/429/500/503/529/absent → auth/rate_limit/server/network as A2.5.
9. `native_status=unavailable` from an identity mismatch → **no** `HARNESS_UNREACHABLE` ever; an
   unrecognized status type → no; continuous read failures for 120 s → yes, `unavailable_since` set;
   a successful probe clears it. The read-failure marker is the reconcile failure path only.
10. A pending permission suppresses every reason.
11. Retry `next` past with an unchanged attempt for ≥ `no_progress_s` → `retry_overdue=true`; an
    advanced attempt or a future `next` clears it; the overdue signal is not `NO_PROGRESS` and not a
    PASS.
12. Claude `system/api_retry` → `PROVIDER_RETRYING` with attempt/max/next and the mapped hint; a
    `no_response` object → `network`; `overloaded` → `server`; a later event clears the retry state.
13. Claude `tool_use` without `tool_result` → running tool; `tool_result` clears it; over `tool_long_s`
    while the child runs → `TOOL_RUNNING_LONG`; a reader/stream failure (not an `api_retry`) is the
    only source of `HARNESS_UNREACHABLE`.
14. Malformed/unknown events, oversized parts/lines and hostile timestamps are ignored without panic;
    a timestamp after `observed_at` is ignored.
15. No native mutation: zero Prompt/Abort calls; state, guard, idempotency and replay unchanged; a
    different payload still conflicts.
16. JSON compatibility: old parsers decode the block; new fields optional and omitempty.
17. For Variant A: a retry never sets `stall_suspected`; for Variant B (only if Q1 chooses it): test 3
    and CONTRACTS are superseded and a retry sets it. The suite is parameterized so the chosen variant
    is explicit, not implied.
18. If Q3 later adds `liveness_thresholds`: same key + different thresholds →
    `IDEMPOTENCY_CONFLICT`; a replay returns the recorded thresholds. (Not in Option 1.)
19. `go test -race -count=20` on the touched packages with concurrent probes/polls.

Mutations (must be killed): retry reported as `NO_PROGRESS` or never reported; 429 mapped to `server`;
a running tool does not suppress `NO_PROGRESS`; a stale running part reports `TOOL_RUNNING_LONG`;
identity mismatch produces `HARNESS_UNREACHABLE`; `unavailable_since` lost per probe; `retry_overdue`
never set; the free-text message copied into a field; a stale retry reason not cleared; liveness
touching state/guard; the Claude `retry_max` not carried; `no_response` mapped to `unknown`; the
dashboard rendering a raw tool name untruncated; a replay flag or threshold fingerprint wrong.

### A2.13 Slices (one PR each; docs first where the contract changes)

1. **S1 — OpenCode structured retry/error:** parse status `message`/`action` and a `retry` part if
   present; `PROVIDER_RETRYING`/`PROVIDER_ERROR` in the Q1-chosen field; `cause_hint`/`cause_source`;
   `retry_overdue` with the attempt-unchanged rule; CONTRACTS update.
2. **S2 — running tool:** `tool` object, `TOOL_RUNNING_LONG` under `busy`, NO_PROGRESS suppression and
   the absent-status case.
3. **S3 — unreachable + operator thresholds:** the dedicated read-failure condition,
   `unavailable_since` on the run, operator-configured thresholds; **not** the per-execution input
   (Q3).
4. **S4 — Claude mapping:** decode `api_retry`/`no_response`, add the `tool_use` id, tool pairs,
   liveness for Claude executions, the reader-failure condition; no argv change.
5. **S5 — dashboard:** badges/detail and poll JSON; read-only; Variant A separates degraded from
   stalled.

Each slice is RED-first with the cases/mutations it owns; no live service is needed for acceptance.

### A2.14 Open questions for the maintainer

1. **Q1 (blocking): `stall_suspected` vs a routine retry.** Variant A (separate
   `degradation_reason`/`degraded`, A1 unchanged) or Variant B (move the provider reasons into
   `stall_reason` and supersede A1 test 3/CONTRACTS)? A2 does not decide; S1–S5 need the answer.
2. **Q2 tool name exposure:** bounded 64-byte name or only a class/hash? (Proposed: bounded name,
   never in audit.)
3. **Q3 per-execution thresholds:** keep Option 1 output-only (operator config), or add a
   `send_prompt` input later? If added, it is `liveness_thresholds`, part of the idempotency
   fingerprint (different thresholds → `IDEMPOTENCY_CONFLICT`), and a separate decision/slice.
4. **Q4 `HARNESS_UNREACHABLE`:** time-based (`unavailable_s`, recommended) or N consecutive failed
   probes? Probe cadence varies.
5. **Q5 free-text hints:** keep structured-only (Option 1) for v2, or accept Option 2's closed-pattern
   fallback to close the OpenCode network gap? (Option 1 alone does not distinguish network for a
   status-only retry.)
6. **Q6 Claude 2.1.325:** require a live confirmation of the `api_retry`/`no_response` field set before
   S4, or accept the lenient parser with `unknown`?
7. **Q7 OpenCode retry parts:** is a `retry` part persisted while a retry is pending, or only after?
   (UNVERIFIED; a live check would make network attribution possible via the typed `APIError`.)
8. **Q8 cause precedence:** if `action.reason` and the typed error disagree, newest wins — confirm.
9. **Q9 dashboard wording:** the badges (`retrying`, `overdue`, `provider error`, `tool running`,
   `stalled`, `unreachable`) and `unknown` instead of a guess.
10. **Q10 per-tool defaults:** one global `tool_long_s`, or per-tool defaults (for example Bash longer
    than Read)?
11. **Q11 `retry_overdue`:** the grace (`max(30 s, no_progress_s)`) and the attempt-unchanged window;
    should an overdue retry be a separate `PROVIDER_RETRY_OVERDUE` reason?
12. **Q12 Claude `tool_long_s` vs `RUN_MAX`:** cap the threshold at `RUN_MAX` automatically, or leave
    it to the operator and document that a larger value never fires?
