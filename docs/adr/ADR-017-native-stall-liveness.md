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

## Amendment A2 (PROPOSED, 2026-11-21): stall and degradation taxonomy v2 (network, provider, hung subprocess)

Status: **PROPOSED, not accepted.** This amendment answers the maintainer need of 2026-11-21: in
daily OpenCode / Claude Code use the network drops, the LLM provider connection fails or keeps
retrying, or a child process the agent started hangs or runs much longer than expected, and the
supervisor (Claude Chat, through the Bridge) must be able to tell these apart to choose wait,
interrupt or follow-up. It is additive to the accepted A1 (option (a)); until it is accepted the
Bridge serves exactly the A1 block. It preserves ADR-010 and ADR-011: **report-only**; no automatic
Abort, resubmission, guard release, new idempotency key or inferred success; recovery from a
confirmed stall remains the ADR-010 fenced procedure.

### A2.1 Pinned sources and what the Bridge reads today

**OpenCode 1.19.4**, `anomalyco/opencode` tag `v1.19.4`, revision
`148842ef7cb1bc34192b3fff57d0f9721ab9c801` (the revision A1 §2 already uses). Additional facts
read at that revision on 2026-11-21 (paths relative to the repository root):

| Fact | Source | Status |
| --- | --- | --- |
| `session.status` retry carries `attempt`, `message`, `action{reason, provider, title, message, label, link?}`, `next` | `packages/schema/src/session-status-event.ts:13-28` | verified (source) |
| `RetryReason` is `"free_tier_limit" | "account_rate_limit" | (string & {})`: an **open** string; the two named values come from usage-limit bodies | `packages/opencode/src/session/retry.ts` (`RetryReason`, `Retryable`) | verified (source) |
| Retryable failures are classified by the retry policy from typed `APIError` (status code, isRetryable, message, headers, body) and from message patterns; a non-retryable error is not retried | `packages/opencode/src/session/retry.ts` (`retryable`, `policy`) | verified (source) |
| Assistant error union: `ProviderAuthError{providerID,message}`, `UnknownError{message,ref?}`, `MessageOutputLengthError`, `MessageAbortedError`, `StructuredOutputError{message,retries}`, `ContextOverflowError`, `ContentFilterError`, `APIError{message,statusCode?,isRetryable,responseHeaders?,responseBody?,metadata?}` | `packages/schema/src/v1/session.ts` ("AssistantErrorSchema", `Assistant.error`) | verified (source) |
| The assistant part union includes a `retry` part `{type:"retry", attempt, error: APIError, time.created}` (whether the server persists it while a retry is pending is UNVERIFIED; A2 uses it only if present) | `packages/schema/src/v1/session.ts` (`RetryPart`) | verified (source) |
| Tool part state: `pending`, `running{input,title?,metadata?,time.start}`, `completed{…,time.start/end}`, `error{…,time.start/end}`; a running tool has **no output timestamp** | `packages/schema/src/v1/session.ts` (`ToolStateRunning`, `ToolPart`) | verified (source) |
| `/event` carries `session.status`, `session.error`, `message.part.updated`, `message.part.delta` (A1 N12) | `packages/schema/src/session-status-event.ts`, `…/v1/session.ts` | verified (source); not consumed by the Bridge |
| The Bridge reads `Status{type,attempt,next}` only; `message`/`action` are dropped | `internal/adapters/opencode/client.go:97-104` | verified (code) |
| Liveness progress uses assistant `time.created/completed` and text/reasoning/tool part timestamps; unknown part types (including `retry`) are ignored | `internal/execution/liveness.go:80-115,151-170` | verified (code) |
| The assistant error **name** is read for terminal proof (`MessageAbortedError`→interrupted, any other named error→failed) | `internal/execution/reconcile.go:440-464` | verified (code) |
| The probe already reads status, history and permissions; A2 adds no native read | `internal/execution/reconcile.go:216-345` | verified (code) |

**Claude Code 2.1.325.** The runner already spawns the CLI with `--output-format stream-json
--verbose` (`internal/adapters/claudecode/argv.go:160-164`), so the `system/api_retry` event is
already on the stream. The decoder keeps `Event.Type`/`Subtype` but parses only `system/init` and
`result` (`internal/adapters/claudecode/decoder.go:82-91,180-215`); the state machine ignores other
system subtypes (`internal/adapters/claudecode/state.go:218-244`); no liveness is served for a
Claude execution. The event fields below are from the public reference
`code.claude.com/docs/en/headless` (retrieved 2026-11-21), not from the pinned 2.1.325 binary:

| `system/api_retry` field | Type | Use |
| --- | --- | --- |
| `attempt` | integer, from 1 | retry count |
| `max_retries` | integer | bound for this failure (may be lower than the session budget) |
| `retry_delay_ms` | integer | delay before the next attempt |
| `error_status` | integer or null | HTTP status; **null means no HTTP response** (connection-level) |
| `error` | enum string | documented categories: `rate_limit`, `server_error`, `authentication_failed`, `billing_error`, `invalid_request`, `max_output_tokens`, `unknown` |
| `uuid` | string | event identity |

Marked **UNVERIFIED**: the exact 2.1.325 field set (docs may drift), and third-party reports that an
`apiKeyHelper` 401/403 can be retried silently for the first two attempts (if true, absence of
`api_retry` is never evidence of no retry). The mapping in A2.7 is lenient for that reason.

### A2.2 What v1 cannot tell apart (gap table against the maintainer need)

| Case | v1 signal today | v2 report | Still impossible |
| --- | --- | --- | --- |
| Network down (Bridge→provider, while running) | status `retry` with a free-text message, or `busy`, or `unavailable`; A1 reports `NO_PROGRESS` only after 2 min | `PROVIDER_RETRYING` (retry status) or `PROVIDER_ERROR` (typed error without terminal proof), `cause_hint=network` only when the structured status is absent (OpenCode `APIError.statusCode` missing) or `error_status=null` (Claude) | Without a status code, "network" vs an opaque 5xx can be indistinguishable from the API payload; the hint is a class, not a diagnosis |
| Provider retrying | `native_status=retry`, `retry_attempt`, `retry_next_at`; no reason | `PROVIDER_RETRYING` + `cause_hint` from `action.reason` or the typed error; `retry_max` for Claude | The total retry budget is not exposed by OpenCode; a "watchdog" mode can retry for hours (config-dependent) |
| Provider hard error | terminal `failed` if the message carries a named error; otherwise `unknown`/`MONITOR_EXPIRED` (R3a) | If terminal: unchanged. If no terminal proof: `PROVIDER_ERROR` + `cause_hint` + `cause_source` | Whether an error without terminal evidence means the native turn ended: only a live repro (R3a N5/N8) would tell |
| Hung tool child | `busy` + part timestamps; `NO_PROGRESS` at 2 min, which mislabels a legitimately long tool | `TOOL_RUNNING_LONG{tool_name, running_since}`; `NO_PROGRESS` suppressed while a tool runs | No native liveness inside a tool: no output/heartbeat timestamp for a running tool (OpenCode schema has none); "hung" vs "busy build" is intent |
| Legitimately long tool (a build) | same as above | same signal, plus the per-execution threshold override; the tool name and elapsed time let a human decide | The Bridge must not guess intent; a low threshold + long build still shows `TOOL_RUNNING_LONG` (advisory) |
| Model thinking a long time | `busy` + no completed part; `NO_PROGRESS` at 2 min | `NO_PROGRESS` (unchanged class; the reason names only "no progress") | No signal distinguishes thinking from a hung model/provider call in either harness; state the ambiguity rather than invent one |
| Bridge→harness connection lost | `native_status=unavailable`; no reason; a single failed probe clears the block | `HARNESS_UNREACHABLE` after a sustained unavailable window, with `unavailable_since` | Local/remote cause (harness down, socket, Bridge permission, network) is not in the error surface; no retry can prove the old turn ended |

No case gains an automatic action.

### A2.3 v2 fields (additive, optional, closed sets, report-only)

All A1 fields keep their names and meanings. New optional fields:

| Field | Type / values | Meaning |
| --- | --- | --- |
| `stall_reason` | A1 values plus `PROVIDER_RETRYING`, `PROVIDER_ERROR`, `TOOL_RUNNING_LONG`, `HARNESS_UNREACHABLE` | The one reason this probe reports; closed set; `stall_suspected` stays `true` iff a reason is present |
| `cause_hint` | `network`, `rate_limit`, `auth`, `server`, `unknown` | Coarse cause of a provider problem, derived as in A2.5; never free text |
| `cause_source` | `structured`, `bounded_text`, `none` | Provenance of `cause_hint`; v2 (Option 1) emits only `structured` or `none` |
| `retry_max` | integer, optional | Claude `max_retries`; absent for OpenCode (not exposed) |
| `tool` | object, optional | `{tool_name, running_since, last_output_at?}`; the oldest running tool of the newest correlated assistant |
| `tool.tool_name` | string ≤64 bytes, `[A-Za-z0-9._-]` or `other` | Native tool name, sanitized; untrusted display data, never authority |
| `tool.running_since` | RFC3339 (native `state.time.start`, never after `observed_at`) | When the running tool started |
| `tool.last_output_at` | RFC3339, optional | **Never present for OpenCode 1.19.4** (no such field); Claude: omitted (no per-tool output timestamps without partial messages) |
| `unavailable_since` | RFC3339, optional | First observation of the current `unavailable` window (Bridge clock) |
| `thresholds` | object | `{no_progress_s, tool_long_s}` actually applied to this execution |
| `progress_source` | `native`, `bridge` | OpenCode: `native`; Claude: `bridge` (no native per-event timestamps in the stream) |

The block is still absent before submission and on terminal executions, process-local, and never
changes `state`, `ended_at`, `unresolved`, `error`, the guard or idempotency. A client that does not
know the fields is unaffected (the object is already additive).

### A2.4 Precedence and exact definitions

A pending native permission suppresses **every** reason (A1 unchanged). Otherwise the first match
wins:

1. **`HARNESS_UNREACHABLE`** — this probe's own native reads failed (`native_status=unavailable`,
   the existing failure signal) and the execution has been continuously unavailable for
   `unavailable_s` (default 120 s). `cause_hint=unknown`; `unavailable_since` set. A single failed
   probe reports `native_status=unavailable` with no reason, exactly as A1.
2. **`COMPLETED_WITHOUT_TERMINAL_EVIDENCE`** — A1 definition unchanged (completed assistant, no
   finish stop/end_turn, no named error, status absent, no pending permission).
3. **`NO_CORRELATED_ASSISTANT`** — A1 definition unchanged.
4. **`PROVIDER_ERROR`** — the execution is unresolved (no terminal proof), `native_status` is not
   `retry`, and a typed provider error is available from a source in A2.5 (OpenCode assistant
   `error` or a `retry` part if present; Claude terminal-error result events are terminal and cleared).
   `cause_hint` from that error.
5. **`PROVIDER_RETRYING`** — `native_status=retry` (OpenCode) or the Claude retry state is active
   (A2.7). `retry_attempt`/`retry_next_at`/`retry_max` accompany it; `cause_hint` from the retry
   payload. A future `next` is normal, not a stall: this reason is the "wait" signal.
6. **`TOOL_RUNNING_LONG`** — the newest correlated assistant has a `tool` part with
   `state.status=running` and `now - state.time.start ≥ tool_long_s` (default 600 s) and no later
   completed/error tool part for the same call. `tool` is set. This suppresses `NO_PROGRESS`.
7. **`NO_PROGRESS`** — `native_status=busy` with no native progress (A1 `last_native_progress_at` or
   submission) for longer than `no_progress_s` (default 120 s), and no running tool (item 6). A2
   supersedes A1's "retry whose `next` is past" branch: every retry status is
   `PROVIDER_RETRYING` (item 5), so a past `next` is reported as a retry that is due, not as
   `NO_PROGRESS`.

`stall_suspected` keeps its A1 invariant (true iff a reason is present) and therefore also marks
an active retry. A2 defines that as **degradation**, explicitly not a stall and never terminal; the
supervisor keeps using `stall_reason`/`native_status`, not the boolean alone. (Open question 1 asks
whether a separate `degradation` flag is preferable.)

### A2.5 `cause_hint` mapping (recommended: structured fields only)

Structured sources, newest applicable first; the hint is one closed word and `cause_source` records
where it came from. **No text is ever exported**; `message`, `action.title`, `responseBody`, tool
names and provider names never enter the liveness block.

| Source (OpenCode) | Mapping |
| --- | --- |
| `status.retry.action.reason` | `free_tier_limit`, `account_rate_limit` → `rate_limit`; anything else (open string) → `unknown` |
| `assistant.error.name == ProviderAuthError` | `auth` |
| `assistant.error.name == APIError` (or a `retry` part's `error`, same `{name,data}` shape) | `data.statusCode` 401/403 → `auth`; 429 → `rate_limit`; 408/500/502/503/504/529 → `server`; absent/0 → `network`; any other → `unknown` |
| `assistant.error.name` in `ContextOverflowError`, `ContentFilterError`, `MessageOutputLengthError`, `StructuredOutputError`, `MessageAbortedError`, `UnknownError` | `unknown` (not a provider-transport cause) |

| Source (Claude `api_retry`) | Mapping |
| --- | --- |
| `error_status` non-null | 401/403 → `auth`; 429 → `rate_limit`; 408/5xx → `server`; else `unknown` |
| `error_status == null` | `network` |
| `error` (documented enum) with no status | `rate_limit`→`rate_limit`; `authentication_failed`→`auth`; `server_error`→`server`; `billing_error`/`invalid_request`/`max_output_tokens`/`unknown`/anything else → `unknown` |

Option 2 (not recommended for v2) would add a bounded-text fallback: lowercase at most 4 KiB of the
retry message, match a **closed** pattern set (429/rate limit, 5xx/overloaded, DNS/TLS/connection,
401/403, timeout), and set `cause_source=bounded_text` without exporting the text. It duplicates the
upstream retry policy, is version-brittle, and is unnecessary while the structured sources exist.

### A2.6 Thresholds and per-execution override

| Threshold | Default | Bounds (proposed) | Scope |
| --- | --- | --- | --- |
| `no_progress_s` | 120 s (A1) | 30–900 s | one execution |
| `tool_long_s` | 600 s | 60–3600 s | one execution |
| `unavailable_s` | 120 s | fixed in the first slice | operator/config (not per execution) |

The per-execution override is a new optional input on `send_prompt`, for example
`liveness: {"no_progress_s": 600, "tool_long_s": 1800}`: validated by the Bridge, recorded on the
execution, echoed in `liveness.thresholds`, immutable after admission, and returned unchanged by a
replay. Absent → defaults. Out-of-bounds values are `INVALID_INPUT` (never silently clamped). A
threshold never changes `state`, the guard or a native call, and it never retroactively changes an
execution. A1's "the threshold is fixed in this release" note is superseded only for executions
created after the override exists.

### A2.7 Claude Code mapping

The runner already streams the events; A2 adds a decoder/state lookup, no argv change (no
`--include-partial-messages`; text/thinking deltas stay unavailable, so a long model turn can only
be `NO_PROGRESS`).

| Event / state | Liveness |
| --- | --- |
| Child admitted, before `init` | no block (as A1 before submission) |
| `system/init` seen | `native_status=busy`; `progress_source=bridge`; start time |
| `system/api_retry` | `PROVIDER_RETRYING`; `retry_attempt=attempt`, `retry_max=max_retries`, `retry_next_at=now+retry_delay_ms` (Bridge clock); `cause_hint` per A2.5; a later event of any kind clears the retry state |
| `assistant` with a `tool_use` block | running tool: `tool_name`, `running_since` = Bridge receive time; `TOOL_RUNNING_LONG` after `tool_long_s` |
| `user` with the matching `tool_result` | tool cleared; progress updated |
| `stream_event` (only if a later slice enables partial messages) | would update progress; not in v2 |
| `result` / terminal verdict (existing rows) | execution terminal; liveness cleared by A1's rule |
| broken/undecodable stream bounded out, or the reader stops | `native_status=unavailable`; `HARNESS_UNREACHABLE` after `unavailable_s`; the existing child/verdict logic is untouched |
| no native status endpoint | `native_status=busy` while the child runs; `absent` is never emitted for Claude (there is no status map to be absent from) |

`progress_source=bridge` makes explicit that Claude progress times are the Bridge's receive clock,
not native timestamps. Liveness is served on `get_execution_status`, `get_execution_result` and the
replay response, like OpenCode, and is process-local.

### A2.8 Dashboard display

Execution list/detail rows gain a read-only badge: `retrying` (attempt/max, next), `tool running`
(name, elapsed), `stalled` (reason `NO_PROGRESS`/`TOOL_RUNNING_LONG`), `provider error` (cause),
`unreachable` (since). The detail shows `stall_reason`, `cause_hint`, `cause_source`, the retry
fields, the tool fields and `observed_at`; the existing poll JSON gains the same optional fields.
No new route, no write, no token, no free text; an unknown value renders `unknown`, never a guess.
Archived-session behavior and the audit ring are unchanged.

### A2.9 Supervisor guidance (unchanged safety rules)

| Signal | Wait | Interrupt | Follow-up |
| --- | --- | --- | --- |
| `PROVIDER_RETRYING` | until `retry_next_at` + one probe; attempts may grow (watchdog) | no | only after the execution is terminal |
| `PROVIDER_ERROR` | read status/result/history; a terminal verdict wins | no | only after terminal; unknown → fenced recovery |
| `TOOL_RUNNING_LONG` | yes for a known build (raise the threshold on the next prompt) | explicit supervisor decision if it persists | only after terminal |
| `NO_PROGRESS` | one more probe interval | if it persists and no tool is running | no |
| `HARNESS_UNREACHABLE` | retry the poll; check the harness/network | no (the Bridge cannot signal a child it cannot see) | no |
| `COMPLETED_WITHOUT_TERMINAL_EVIDENCE` | no | no | ADR-010 fenced recovery; never a new key while the guard is held |

Never resubmit an uncertain prompt, never reuse a key, never infer success, never release the guard
from liveness; the maximum five autonomous corrective iterations is unchanged; an interrupt stays an
explicit supervisor action.

### A2.10 What stays impossible

- Chat cannot be woken: it only sees this on poll; a silent stall is silent until polled.
- No native signal proves "dead" (A1 §2); `HARNESS_UNREACHABLE` is a network/observation fact, not a
  death certificate.
- Network vs opaque server failure without a status code; "provider error" vs "overflow/filter" are
  distinguishable only by the typed error when the harness exposes it.
- Hung tool vs legitimately long tool, and thinking vs a hung model call, are intent/policy
  questions; the hint narrows them, it does not decide.
- A Bridge restart loses liveness with the execution (NOT_FOUND); old keys stay dead (ADR-010).
- Native clocks can skew; A1's future-timestamp rule stays.
- Claude's silent first retries (if the 2.1.325 behavior matches the third-party report) mean absence
  of `api_retry` is not evidence of a healthy provider.

### A2.11 Options and recommendation

- **Option 1 (recommended): structured-only v2.** The A2.3 fields, A2.4 precedence, A2.5 structured
  mapping, A2.6 thresholds, A2.7 Claude mapping and A2.8 dashboard. No free-text parsing; no new
  native read (status/action and retry parts come from responses already read).
- **Option 2: structured + bounded-text fallback.** Adds the A2.5 fallback (`cause_source=
  bounded_text`). Not recommended for v2: version-brittle, duplicates upstream retry policy, buys
  little over `unknown`.
- **Option 3: consume `/event` for liveness.** Push progress (`message.part.updated`,
  `session.status`, `session.error`) would reduce polling lag and expose transient errors. Deferred:
  delivery/ordering/durability are UNVERIFIED (A1 N12: "the Bridge does not consume these"); it adds
  a subscription and lifetime to the writer, and can be a later amendment that feeds the same
  fields.

Accept Option 1 in slices; keep A1's report-only contract and ADR-010/011 untouched.

### A2.12 Test plan (RED-first, no live services)

Fabricated streams/status/history; no live CLI, provider or host. Each case gets a RED test before
the code, and the mutation below is run against the finished slices.

1. Retry with `action.reason=account_rate_limit` → `PROVIDER_RETRYING`, `cause_hint=rate_limit`,
   attempt/next recorded.
2. Retry with an unknown `action.reason` → `cause_hint=unknown`, no text in any field.
3. `busy` + a running tool 1 s old → no reason; 601 s → `TOOL_RUNNING_LONG`, tool name/start, and
   `NO_PROGRESS` suppressed even past its threshold.
4. `busy`, no tool, 119 s → no reason; 121 s → `NO_PROGRESS`.
5. Completed assistant without finish/error (A1 shape) → `COMPLETED_WITHOUT_TERMINAL_EVIDENCE`
   (unchanged), state/guard unchanged.
6. Assistant `ProviderAuthError` with no terminal proof → `PROVIDER_ERROR`, `cause_hint=auth`.
7. Typed APIError statuses 401/403/429/500/503/529/none → auth/rate_limit/server/network as A2.5.
8. All reads fail → `native_status=unavailable` (no reason); after `unavailable_s` →
   `HARNESS_UNREACHABLE` + `unavailable_since`; a successful probe clears it.
9. A pending permission suppresses every reason.
10. Threshold override: valid values recorded and echoed on replay; 29/901 s and 59/3601 s are
    `INVALID_INPUT`; the execution's state/guard never change.
11. Claude `system/api_retry` stream → `PROVIDER_RETRYING` with attempt/max/next and the mapped hint;
    a later event clears the retry state.
12. Claude `tool_use` without `tool_result` → running tool; `tool_result` clears it; over
    `tool_long_s` → `TOOL_RUNNING_LONG`.
13. Malformed/unknown events, oversized parts/lines and hostile timestamps are ignored without panic;
    a timestamp after `observed_at` is ignored.
14. No native mutation: zero Prompt/Abort calls across all probes; state, guard, idempotency and
    replay unchanged; a different payload still conflicts.
15. JSON compatibility: old parsers decode the block; new fields optional and omitempty.
16. `go test -race -count=20` on the touched packages with concurrent probes/polls.

Mutations (must be killed): retry never reported (old behavior); 429 mapped to `server`; tool
condition dropped so `NO_PROGRESS` fires during a build; `unavailable_s` ignored; the free-text
message copied into a field; a stale retry reason not cleared; liveness touching state/guard; the
Claude `retry_max` not carried; the dashboard rendering a raw tool name untruncated.

### A2.13 Slices (one PR each, docs first where the contract changes)

1. **S1 — OpenCode structured retry/error:** parse status `message`/`action` and persisted `retry`
   parts; `PROVIDER_RETRYING`/`PROVIDER_ERROR`; `cause_hint`/`cause_source`; CONTRACTS update.
2. **S2 — running tool:** `tool` object, `TOOL_RUNNING_LONG`, NO_PROGRESS suppression.
3. **S3 — unreachable + thresholds:** `HARNESS_UNREACHABLE`, `unavailable_since`, the per-execution
   override and its bounds.
4. **S4 — Claude mapping:** decode `api_retry`, tool pairs, liveness object for Claude executions;
   no argv change.
5. **S5 — dashboard:** badges/detail and poll JSON; read-only.

Each slice is RED-first with the A2.12 cases and mutations it owns; no live service is needed for a
slice to be accepted (the live run only confirms the upstream event shapes).

### A2.14 Open questions for the maintainer

1. **Retry and `stall_suspected`:** is `stall_suspected=true` acceptable for an active provider
   retry (a degradation, not a stall), or do you want a separate `degradation` flag? Option 1 above
   assumes the former with `stall_reason` naming the state.
2. **Tool name exposure:** bounded 64-byte name or only a class/hash? Option 1 exposes the bounded
   name (never in audit).
3. **Per-execution thresholds:** new `send_prompt` input fields as in A2.6, or operator config only?
   The sitting's long builds are the reason for the former.
4. **`HARNESS_UNREACHABLE`:** time-based (`unavailable_s`, recommended) or N consecutive failed
   probes? Probe cadence varies.
5. **Free-text hints:** keep structured-only (Option 1) for v2, and revisit Option 2 only with
   evidence that the structured fields are insufficient?
6. **Claude 2.1.325:** do we need a live confirmation of the `api_retry` field set (and the silent
   first retries) before S4, or is a lenient parser with `unknown` sufficient? (The docs are the
   only source I could read; the pinned binary was not run.)
7. **OpenCode retry parts:** is a `retry` part persisted while the retry is pending, or only after?
   The schema allows it; whether the server writes it mid-retry is UNVERIFIED. S1 must treat it as
   optional.
8. **Cause precedence:** if the status `action.reason` and the persisted APIError disagree, which
   wins? A2.5 uses the newest applicable source; confirm that is the intended tie-break.
9. **Dashboard wording:** the five badges above, and `unknown` instead of a guess; any labels to
   avoid in operator-facing text?
10. **Per-tool defaults:** one global `tool_long_s`, or per-tool defaults (for example Bash longer
    than Read)? Option 1 keeps one global default plus the override.
