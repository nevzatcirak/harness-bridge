# ADR-021: Claude Code adapter

Date: 2026-11-11. Status: **ACCEPTED (per D12, 2026-11-12)**; the open questions
are answered by maintainer decision D14 (section "Decided (D14, 2026-11-12)").
This ADR itself writes no code; implementation proceeds in the slices of §10
(slice 1 first), and nothing is deployed or enabled.

Maintainer decisions recorded 2026-11-11 and binding for this proposal:

- **D7 (history, option (b)):** the adapter reads history in **pure Go from the
  stream-json output of runs the Bridge itself starts or resumes**. There is no
  Node or Python helper and no JSONL parsing. For a session created outside the
  Bridge, its **history** is visible only from the moment the Bridge first
  resumes it. This is a limit on history, not on listing: a session on the
  operator allowlist (§6) is listed from startup.
- **D5 (auth):** subscription login under a non-root service user (the
  `hbspike` wrapper pattern of the P6 spike). Never `--bare`.
- D4 (minimal spike GO) is already executed: see the spike evidence below.

Must preserve ADR-010 (single writer, memory-only records), ADR-011 (late
completion, no success from idle), ADR-017 option (a) (report-only liveness),
ADR-018 option (d) (report-only permission rejection) and ADR-020 option (b)
(operator-only fenced guard release), all accepted. It amends none of them as
written. It carries one extension of ADR-020, scoped to the Claude harness only
(§3, last bullet; slice 4; allowed by D14 Q8): ADR-020's native-read
preconditions have no Claude equivalent, so a Claude-specific set is used. The
extension is implemented in slice 4 and takes effect then. ADR-024 (permission relay) is scoped to OpenCode and
does not cover Claude Code: its draft states that relay is not possible under the
current Claude Code CLI contract (per review #172 of this ADR; draft PR #162, its
section 7). Any approval path for Claude Code would need its own future ADR after
native-API evidence. ADR-022 (session lifecycle) is not used here; ADR-023
(read-only dashboard) is a separate proposal (see the last sentence of §5).

## Evidence base and how claims are marked

| Source | Used for |
| --- | --- |
| `docs/research/P6_CLAUDE_CODE_FEASIBILITY.md` (2026-11-07, docs only, CLI 2.1.319) | boundary, doc citations, open questions |
| `docs/evidence/P6_SPIKE_EVIDENCE_2026-11-10.md` (CLI **2.1.322**, subscription OAuth via `hbspike`, aarch64, 23 runs) | observed behavior; cited below as "spike step N" |
| ADR-010/011/017/018/020 | Bridge-side invariants this adapter must keep |
| Official docs, fetched for this ADR (access date 2026-11-11) | flag and setting claims in §8; URL and date given per claim |

Marking, used throughout: **OBSERVED** = seen in the spike on 2.1.322;
**DOCUMENTED** = in an official page cited with URL and access date;
**UNVERIFIED** = neither; the implementation plan (§10) names the slice that
must verify it before the claim may be relied on. Anything marked UNVERIFIED is
a risk, not a feature.

What the spike established that shapes this design (all OBSERVED on 2.1.322):

1. **No native writer lock.** Two concurrent `-p --resume A` both ran, exit 0, no
   refusal. A later history listing contained one of the two turns but not the
   other, and **which turn was missing differed between the two runs**
   (spike step 5; "last writer wins" was explicitly not established). The loss
   itself is **inferred from the model's own listing**, not read from a
   transcript. So the Bridge fence is the only protection against the Bridge's
   own concurrent writers, and a human terminal writer cannot be excluded.
2. **Resume is not bound to the creating directory.** `--resume A` from a second
   directory found the session, kept the `session_id` and ran with the **invoking**
   directory as `init.cwd` (step 2). The Bridge must therefore choose the cwd
   itself and check it against `init.cwd`.
3. **Interrupt results (via the `sudo` wrapper, wrapper exit codes only):**
   SIGINT during a tool call produced `result` with
   `subtype: "error_during_execution"`, `is_error: true`,
   `terminal_reason: "aborted_tools"`, wrapper exit **0**. SIGTERM produced exit
   **143** and **no `result` line**. The session resumed normally after both, and
   the model's account of the killed tool differed between the two cases
   (step 6). Raw shell captures of these exit codes were not retained.
4. **Failures are distinguishable from success only by the `result` line plus
   the exit code together:** `error_max_turns` gave exit 1; an unknown session ID
   gave exit 1, stderr `No conversation found with session ID: <id>`, a single
   `result` line with `error_during_execution`, `num_turns: 0`, **no
   `system/init`**, cost 0 (steps 3, 4).
5. **A denied tool is a success-shaped result.** `subtype: "success"`,
   `is_error: false`, exit 0, with the denial only in `permission_denials[]`
   (step 7). Callers must read `permission_denials`.
6. **A tool that the runtime refuses or backgrounds is reported inside the
   transcript**, not as a process failure (a bare `sleep` is blocked by the CLI
   and backgrounded, spike step 6 notes). Process exit never describes tool outcomes.
7. **Stdin.** Without redirected stdin the CLI waits 3 s and warns
   (`no stdin data received in 3s`); the spike redirected stdin from `/dev/null`.
   The prompt delivery channel is therefore a design decision (§1), not a given.
8. `system/init` carries `session_id`, `cwd`, `model`, `permissionMode`,
   `apiKeySource` (`none` for subscription OAuth), `claude_code_version` and a
   `capabilities` list. The spike's Write attempt was auto-denied and nothing was
   written (step 7).

## Decision summary

Add a `ClaudeCodeAdapter` behind the existing execution `Runner` seam, as a
second harness next to OpenCode. Session IDs are `claudecode:<uuid>`. Every
execution is exactly one child process of the Claude Code CLI, run as a separate
non-root OS user through a fixed wrapper, resuming one exact session ID in the
session's approved root, with permissions pinned to deny. The Bridge, not Claude
Code, owns the single-writer lock, the execution and idempotency records and the
history record. Outcome decisions use only the child's own stream-json output and
its exit status. Nothing is inferred from `~/.claude`, and nothing reads the CLI's
transcript files.

Consequences stated up front, because they differ from OpenCode:

- There is **no native read path**. ADR-011's late reconciliation, ADR-017's
  liveness probe and ADR-020's native-evidence preconditions have no native API
  to call. The adapter replaces them with facts the Bridge itself observed about
  its own child (§3, §4). An outcome the Bridge did not observe stays `unknown`.
- A session's history exists only for turns the Bridge ran (§5).
- Exclusion of a **human** writing to the same session in a terminal is not
  possible (§2). It is a documented residual risk.

## 1. Process model

**One child per execution.** `send_prompt` admits an execution (ADR-010
admission and ADR-014 identity rules unchanged), then the adapter starts:

```text
<wrapper> -p --resume <uuid> --output-format stream-json --verbose \
          --model <configured> --permission-mode default --permission-prompts none \
          --max-turns <N> [--max-budget-usd <X>] [hardening flags of §8]
```

- `<wrapper>` is an absolute path from `BRIDGE_CLAUDE_COMMAND`. It is **never**
  found through `PATH`, never a shell string, and the Bridge starts it with
  `exec.Command(path, argv...)` (no shell). At startup the Bridge refuses the
  adapter unless the file is a regular file, not a symlink, owned by root, mode
  without group or other write, and in a directory with the same property.
- The wrapper is the `hb-p6-claude` pattern of the spike: a root-owned,
  few-line script that runs the CLI **as the Claude service user**, for example
  `exec sudo -n -u <claude user> -H -- <absolute path of claude> "$@"`. The
  Bridge's own service user is a **different** user from the Claude service user
  (§8). A sudoers rule lets the Bridge user run exactly that wrapper target and
  nothing else, with `env_reset`.
- **Never `--bare` (D5).** `--bare` does not read subscription credentials
  (spike precondition notes; DOCUMENTED, see §8 for the URL). A run that reports
  `init.apiKeySource` other than `none` ends the run `unknown` with
  `IDENTITY_MISMATCH`, guard kept, child interrupted (§3 row 0, §8.2, §8.3 item 4).
- **Pinned permission flags, every run, no exceptions:** `--permission-mode
  default` and `--permission-prompts none`. Never `auto`, `acceptEdits`,
  `bypassPermissions`, `dontAsk` or `--dangerously-skip-permissions`. The Bridge
  fixed-compares the argv it builds against an allow-pattern before `exec` (unit
  test, §10), and a run whose `init.permissionMode` is not `default` ends
  `unknown` with `IDENTITY_MISMATCH`, guard kept, and the child is interrupted
  (§3 row 0, §4, §8.3 item 4). It is not reported `failed`: the Bridge cannot
  prove what ran under the wrong mode.
- **cwd** is the session's approved root from the allowlist (§6), after the same
  canonical-path and symlink checks the OpenCode path uses (ADR-014). The Bridge
  sets `cmd.Dir` and then requires `system/init.cwd` to equal the canonical root.
  A mismatch ends the execution `unknown` with `IDENTITY_MISMATCH`, guard kept,
  and the child is interrupted (§4), for the same reason as above. The spike showed the CLI follows the invoking directory
  (OBSERVED), so this check is the only thing tying a session to its project.
- **Prompt delivery.** The prompt travels over the child's **stdin**, not argv.
  Reason: argv is visible to other users in `/proc` and `ps`, and `sudo` logs the
  full command line to the system log, so a prompt in argv would be copied into
  logs outside the Bridge's control. Stdin is closed after the write. The headless
  page documents stdin only as piped **data** next to a command-line prompt (its
  section "Pipe data through Claude", with an example of the form `cat file |
  claude -p '<prompt>'`, and a 10 MB cap on piped stdin; DOCUMENTED,
  https://code.claude.com/docs/en/headless, accessed 2026-11-11; the earlier
  quotation of a sentence from that page was dropped because it could not be
  found verbatim, review #172 P3-4). A
  prompt that is **only** on stdin, combined with `--resume` and `--output-format
  stream-json`, is **UNVERIFIED on 2.1.322** (the spike used argv with stdin from
  `/dev/null`). Slice 1 (§10) must verify it first. If it fails, the fallback is a
  fixed short argv prompt that tells the model to act on the piped data, with the
  real prompt on stdin as data, **never** the real prompt in argv. If that also
  fails, the adapter is not built and this ADR is amended. The prompt is bounded
  to the existing 32768-byte contract limit.
- **Environment:** a scrubbed, explicit environment (§8), not the Bridge's.
- **Limits per child:** wall-clock cap (`BRIDGE_CLAUDE_RUN_MAX`, default 15 min,
  the analogue of the 300 s monitor but longer because a turn is the whole run),
  stdout bounded per line (1 MiB) and in total (64 MiB), stderr captured to a
  bounded ring (64 KiB) and never forwarded to clients.
- **Where it runs:** one child per execution; at most one child per session (§2);
  a global cap of concurrent children (default 4) mirrors the reconcile-slot idea
  and bounds memory and subscription use.

## 2. Single writer

**The spike proved there is no native lock** (step 5, OBSERVED). So the Bridge
reuses ADR-010's mechanism and semantics unchanged, with the session identity
namespaced by harness:

- **Lock file:** the same `BRIDGE_WRITER_LOCK_DIR` (same validation: existing,
  absolute, mode 0700, owned by the service user, no final symlinks, regular
  non-hard-linked files, `O_CLOEXEC`, `LOCK_NB`). The lock filename is the hash of
  the full string `claudecode:<uuid>`, so it cannot collide with an OpenCode
  `opencode:ses_...` ID. Startup acquires a lock for every allowlisted Claude
  session (§6) exactly as it does for the OpenCode ACL and fails before opening
  the listener if any is held. Read-only processes take none.
- **In-process guard:** the manager's `busy[session]` reservation before any
  child is started, the same pending-preflight channel, the same
  `SESSION_BUSY` for a different key, the same replay of the same key and payload
  (SHA-256 fingerprint) to the same execution, `IDEMPOTENCY_CONFLICT` for a changed
  payload. Memory-only, lost on restart, same ADR-010 limitation and same
  fenced restart procedure. **No new key-reuse allowance.**
- **Admission preflight differs.** For OpenCode the preflight reads native status,
  history and permissions. For Claude there is nothing to read, so the preflight
  is: guard free, lock held, session in the allowlist, root passes the path
  checks, the wrapper is valid, and the global child cap has room. A failed
  preflight starts no child and releases only its own reservation.
- **Child-start rules:** `UNKNOWN_OUTCOME` begins at the moment the prompt could
  have reached the child. Before that point (the wrapper cannot be executed, the
  `fork/exec` fails, a write to stdin fails with no byte accepted) the run is
  finalized `failed` with `local_failure.code = PRE_SUBMISSION_UNAVAILABLE` and
  the guard is released, exactly as for OpenCode: the prompt was provably not
  delivered. After any byte of the prompt was written, a loss of evidence yields
  `unknown` and the guard stays (§3). The same key never starts a second child.
- **Never fork.** `--fork-session` is not used: it changes the session ID and
  breaks "continue the same session". **Residual risk (accepted and documented, D14 Q1):**
  a human who resumes the session in a terminal interleaves with the Bridge and can
  silently drop a turn. The adapter cannot detect this (no documented detection; the
  transcript is not parsed). Operators keep Bridge-managed sessions out of
  interactive use and do not resume the same session in a terminal concurrently, as
  ADR-010 already requires for OpenCode ("operator exclusivity"); the risk is stated
  in the tool descriptions and in CONTRACTS for the Claude harness. The adapter does
  not refuse sessions it did not create and does not fork (the alternatives that
  were considered, D14 Q1).

## 3. State mapping

Input: the child's NDJSON stdout, its exit status and signal, the Bridge's own
records of what it sent (a signal, a deadline), and nothing else.

**Correlation.** One child is one execution and one prompt, so correlation is by
process identity, not by a native message ID (no user message ID is available in
the stream; **UNVERIFIED** whether `--replay-user-messages` or an input-format
variant would expose one, and not needed). The Bridge accepts events only when
`system/init.session_id` equals the exact requested UUID and `init.cwd` equals the
canonical root. Events from a different `session_id` (for example a subagent or
a forked ID) are ignored for state and logged by count only. The Bridge generates
an `execution_id` and the `message_id` field is the execution's own opaque ID for
Claude (documented in §9).

**Two ordering rules apply before the table.**

- **Init verification latch (review #168 P2-1).** The first `system/init` is
  checked immediately (§8.3 item 4). If any of these is violated the verdict is
  **latched**: `permissionMode` is not `default`; `apiKeySource` is not `none`;
  `cwd` is not the canonical root; `session_id` is not the requested UUID;
  `claude_code_version` is not in the allowlist (§8.5); `mcp_servers` or `plugins`
  is non-empty; `tools` is not exactly the configured profile; or a
  `hook_started`/`hook_progress`/`hook_response`/`plugin_install` event appeared
  before `init` (the headless page lists these startup events, review #190 P3-3).
  The Bridge then interrupts the child (§4), and the verdict is row 0 below,
  **regardless of any later `result`, `terminal_reason` or exit status**. Every other
  row applies only if the latch did not fire.
- **Exit before decision (review #172 P2-1).** Every terminal decision, and every
  automatic guard release (rows 1 to 5), is taken only after stdout EOF **and** the
  `wait` of the child (exit status known), because a `result` line can precede
  process exit and a guard released at the `result` could admit a second child
  while the first is alive (§2). A correlated `result` with the child still alive
  is recorded and the execution stays `running` (guard held) for a bounded exit
  grace (default 10 s, configurable; the value is an unmeasured design choice,
  **UNVERIFIED**: a CLI that needs longer than the grace to exit after its
  `result` would end every run in row 6a, so slice 3 measures the result-to-exit
  lag, see its acceptance list). If the child has not exited by the end of
  the grace, or its exit status cannot be obtained, the **grace latch** fires
  (row 6a below). Rows 1 and 2 refer to a child that never produced a usable
  `init`: they also wait for exit, which is immediate in those cases.
- **Interrupt reasons and latched verdicts (reviews #182 P2-1 and #183 P2-1).**
  Every interrupt the Bridge sends (§4) records exactly one reason: `user`
  (requested through `interrupt_execution`), `cap` (the wall-clock cap), `grace`
  (the exit grace expired), `shutdown` (Bridge `Close`) or `latch` (init
  verification). Five events **latch** a verdict at the moment they happen, not at
  EOF: the init violation (row 0), the cap (row 9), the end of the exit grace
  (row 6a), Bridge shutdown (row 9s) and the exhaustion of a `user` interrupt's
  escalation (row 6b). The earliest latch wins, and a latch can only fire while the run is
  non-terminal: a cap, grace or shutdown event that arrives after a verdict was
  decided is a no-op, so a decided run is never re-latched. After a latch, every later
  `result`, line, `terminal_reason` and the exit status are **audit-only**: they
  are recorded (`late_lines_after_verdict`, `late_result_seen`; see the audit
  extension in §8.4), but they cannot re-enter the table, change the verdict or
  release the guard. A late success is not promoted either (this ADR has no
  promotion path for Claude; only an operator guard release, open question 8,
  frees a held guard). In particular an `aborted_*` `result` that answers a `cap`,
  `grace`, `shutdown` or `latch` interrupt is **not** row 5: row 5 is restricted to
  a `user` interrupt.
- **Aborted reasons (one definition).** Wherever this ADR says `aborted_*`, it means
  exactly the closed set `{aborted_tools, aborted_streaming}` (the two documented
  values the spike observed or the SDK page lists). Any other `terminal_reason` that
  merely begins with `aborted_` (for example `aborted_other`) is an unlisted
  terminal reason: it is not an acknowledgement for row 4, row 5 or the row 6b
  exemption, and falls through to row 10 (`unknown`, guard kept). The code has one
  predicate for this (`abortedReason`).
- **Evaluation order.** The latched rows are evaluated first (the earliest latch
  wins; the printed order below is only a tie-break), then the table in printed
  order: `0, 9, 6a, 9s, 6b`, then `1, 2, 8, 3, 4, 5, 6, 7, 10`. The numbers are
  labels, kept stable so that earlier references stay valid; they are not
  positions. Because the latched rows come first, the cap, the grace and shutdown
  outcomes can no longer be pre-empted by rows 3 to 5, and a late exit after the
  grace cannot match row 3 before 6a.

**Terminal decisions** (the table is the whole decision procedure: the latched
rows first, by time of the latch, then the first match in printed order):

| # | Evidence | State | Notes |
|---|---|---|---|
| 0 | The init-verification latch fired (see above), whatever happened afterwards: success-shaped `result`, `aborted_*` result after the Bridge's interrupt, no `result`, any exit status | `unknown`, `IDENTITY_MISMATCH`, **guard kept**, child interrupted | something other than the pinned configuration ran and the Bridge cannot prove what a hook or tool did. Never `completed`, never `interrupted`, never guard-released automatically; the reason is not replaced by `PROCESS_ENDED_WITHOUT_RESULT` |
| 9 | The Bridge's wall-clock cap fired (**latched** at that moment), whether or not a `result` was seen before or arrives later | `unknown`, `MONITOR_EXPIRED`, guard kept; the child is interrupted with reason `cap` (§4); `child_unconfirmed: true` if the escalation is exhausted | latched: a later success or `aborted_*` `result`, a later exit and any later line are audit-only; never row 3, 4 or 5; never `PROCESS_ENDED_WITHOUT_RESULT` |
| 6a | The exit grace ended (**latched** at that moment): at least one correlated `result` was seen and the child had not exited, or its exit status could not be obtained. Row 6a is a latch: a second `result` or counting event seen before the grace ended does not turn it into row 8 (row 8 needs no latch, and both are `unknown` with the guard kept; "exactly one" is a condition of row 3 only) | `unknown`, new additive reason `EXIT_UNCONFIRMED`, `child_unconfirmed: true`, **guard kept**; the child is interrupted with reason `grace` (§4) | the `result` is recorded but is **not** terminal proof, because the process that wrote it may still be running (§2). Never `completed`, never `PROCESS_ENDED_WITHOUT_RESULT`. Latched: a late `aborted_*` result, a late success with exit 0 and any late line are audit-only; no promotion; only an operator guard release (the ADR-020 extension, D14 Q8) frees the guard Row 6a is also reached when the wait itself fails after a `result` (the exit status cannot be obtained): the machine then requests **no `grace` interrupt and records no interrupt reason**, and the verdict keeps `child_unconfirmed: true`; the runner does not signal such a child (it was already interrupted if an interrupt applied) and never kills it |
| 9s | Bridge shutdown (`Close`) while the run is live (**latched**); the child is interrupted with reason `shutdown` | `unknown`, existing reason `MANAGER_CLOSED`, as today | the guard is memory-only and ends with the process; the open turn gets a `restart_gap` marker on the next start (§4, §5). A later `aborted_*` result is audit-only and is not row 5 |
| 6b | The escalation of a `user` interrupt (SIGINT, bounded wait, SIGTERM, bounded wait; §4) was exhausted with the child still alive and no result whose `terminal_reason` is in the closed aborted set (see "Aborted reasons") (**latched** at that moment) | `unknown`, existing reason `ABORT_UNCONFIRMED`, `child_unconfirmed: true`, guard kept | no SIGKILL of the `sudo` parent; removal of the process is an operator action. A later result or exit is audit-only |
| 1 | The child could not be started, or no prompt byte was accepted by stdin | `failed`, `PRE_SUBMISSION_UNAVAILABLE`, guard released | provably not delivered. **When a child was spawned** (the stdin case) the verdict is decided at once but the guard is released only after that child was **reaped**, because one child per session is an invariant (§2); the runner also sends it a SIGINT. Only a `Wait` that returned **without** `ExitUnknown` counts as a reap. **The runner never sends SIGKILL** (see §4: killing the wrapper orphans the command): a child that ignores SIGINT keeps the guard held until it exits by itself or an operator removes it, and a `Wait` that returns `Exit{Unknown:true}` confirms nothing, so the guard stays held too. In that case row 1 stays the decided row (the exit is audit-only; a run not yet decided ends at row 6a or 10). No in-session release exists for a held row-1 guard: the ADR-020 operator release applies to an `unknown` execution whose child is confirmed exited, so the recovery is a Bridge restart (the guard is memory-only, row 9s). A failed start has no child and releases immediately |
| 1s | A write to the child's stdin failed after some prompt bytes were accepted (decided at that moment, like a latch; later output is audit-only) | `unknown`, `SUBMISSION_UNCONFIRMED` (§9), guard kept | the prompt may have reached the child. Zero bytes accepted is row 1 |
| 2 | No `system/init`, a single `result` with `subtype: error_during_execution`, `num_turns: 0`, `errors[]` containing `No conversation found with session ID`, exit 1, **and no counting event after that `result`** (a trailing line makes it row 8, review #204 P3-1) | `failed`, `NOT_FOUND`, guard released | spike step 4 (OBSERVED); nothing ran. Matching is on `num_turns == 0` plus the message class, not on exact text alone |
| 8 | No latch fired, and two or more `result` lines, or a **counting event** after the final `result`, were seen up to EOF (counting events: **any non-empty stdout line after the final `result` counts, decodable as an event or not** (garbage or a truncated line included; review #204 P3-2), and **every event after the final `result` counts**, including `tool_progress` and `system/api_retry`: a `system/api_retry` after it means another API request and a `tool_progress` after it plausibly means a tool is still running, so excluding them would be fail-open (review #200 P2-1). `tool_progress` and `system/api_retry` are non-counting liveness lines only **before** the final `result`, where they only feed `liveness`; that `tool_progress` is a heartbeat is **UNVERIFIED** on 2.1.322. The ignorable list of trailer types that do not count after the final `result` starts **empty** and is extended only from a live multi-run capture of the post-`result` stream, a slice-1 acceptance item) | `unknown`, guard kept | background work can add turns (DOCUMENTED in the research note; the last-result rule there is UNVERIFIED), so the adapter does not guess. Because decisions wait for EOF and exit (rule above), this row is decidable; a second `result` can no longer arrive after rows 3 to 5 released the guard |
| 3 | After stdout EOF **and** `wait`, no latch fired: `init` correlated, then exactly one `result` with `subtype: success`, `is_error: false`, `session_id` equal, **and** exit code 0 | `completed` | success needs all four. `terminal_reason` is recorded; a value other than `completed` on a success `result` downgrades to `unknown` (never seen; unlisted values are not success) |
| 4 | After stdout EOF **and** `wait`, no latch fired: `result` with `is_error: true` and exit non-zero, no Bridge-sent interrupt of any reason, subtype `error_max_turns`, `error_max_budget_usd`, `error_during_execution` (not item 2) or other | `failed`, code from subtype, `retryable: false` | step 3 (OBSERVED: `error_max_turns` exit 1). Guard released: the turn ended with a result the CLI itself wrote The SIGINT shape (`is_error:true`, `terminal_reason` `aborted_*`) without a `user` interrupt is also row 4 although SIGINT exits 0 (OBSERVED), so row 4 matches `is_error:true` with a non-zero exit **or** a `terminal_reason` in the closed aborted set (see "Aborted reasons"), and requires that no Bridge interrupt of any reason was sent |
| 5 | After stdout EOF **and** `wait`, no latch fired: an interrupt with reason **`user`** (requested through `interrupt_execution`, §4) was sent, then `result` with `is_error: true` and `terminal_reason` in `aborted_tools` or `aborted_streaming` | `interrupted` | step 6 (OBSERVED for `aborted_tools` with exit 0). Exit code is **not** used here: SIGINT exited 0 although `is_error` was true. Without a `user` interrupt this shape is item 4, and an `aborted_*` result that answers a `cap`, `grace`, `shutdown` or `latch` interrupt never reaches this row (those are latched rows 9, 6a, 9s, 0). **Guard released**, on the same basis as row 4: the CLI itself wrote a `result` for the aborted turn, which is the terminal proof (a SIGINT without a `result` is row 6 and keeps the guard) |
| 6 | Process ended and **no `result`** at all (SIGTERM and exit 143, crash, broken or oversized stream, `init` seen but stdout closed; a run killed by the Bridge's own cap is row 9) | `unknown`, new additive reason `PROCESS_ENDED_WITHOUT_RESULT`, or the existing `ABORT_UNCONFIRMED` if the Bridge had sent a `user` interrupt and no proof arrived (§4) | step 6 (OBSERVED: 143 and no `result`). Guard **kept** |
| 7 | `result` correlated and success-shaped (latch did not fire), but exit code non-zero | `unknown`, guard kept | contradictory evidence is never success. (`permissionMode`, `apiKeySource` and `cwd` violations are row 0, not here) |
| 10 | Everything else, including a correlated `result` that arrived **before** a valid `system/init` (detail `result_before_init`; row 3 reads "`init` correlated, **then** exactly one `result`", so such a result is never terminal proof for rows 3, 4, 5 or 7; the no-init row 2 shape is unaffected) | `unknown`, guard kept | default |

- **Broken stream (review #216 P2-1).** If stdout ends on a bound (a line over
  1 MiB or the 64 MiB total) after at least one correlated `result`, the run is
  row 8 (`unknown`, guard kept): the line that broke the stream was never decoded,
  and any non-empty line after the final `result` counts. Without a `result` it is
  row 6. Whitespace-only lines are padding, not events: the decoder skips them and
  they are not counted for row 8. The runner must feed `StepStreamBroken`, if the
  decoder stopped on a bound, **before** `StepEOF` and `StepExit` (the machine decides
  when both were fed); the runner test belongs to slice 2.
- **Idle and exit are never success** (ADR-011): a clean exit without a
  success `result` is item 6/7. `system/session_state_changed` is not requested
  (`CLAUDE_CODE_EMIT_SESSION_STATE_EVENTS` stays unset) and would not be success
  evidence in any case.
- **Denied tools.** `result.permission_denials` is copied into the execution as
  the existing report-only ADR-018-style signal (§7). A success `result` with
  denials is still `completed` for the **execution**, as for any harness; the
  denial list is exposed so a client cannot read "completed" as "the task was
  done" (spike step 7 shows the model reporting the denial in prose).
- **`running`** is reported from the moment `init` is correlated until a terminal
  decision. `submitting` covers spawn to `init`. `waiting_for_permission` is never
  reported for Claude in this ADR (denied, not waited, §7).
- **Liveness (ADR-017 analogue, report-only):** `tool_progress` heartbeats and
  `system/api_retry` events the Bridge itself saw feed a `liveness` block with
  `last_event_at` and `last_heartbeat_at`. `stall_suspected` when no event
  arrived for a configured threshold. It never changes `state`, never ends the run
  and never releases a guard. (Heartbeat shape is DOCUMENTED in the research note
  and not exercised in the spike: UNVERIFIED on 2.1.322.)
- **No late reconciliation.** There is no ADR-011 on-demand native read. A
  `unknown` execution stays `unknown`. Only the ADR-020 admin release (as extended by the Claude-harness extension below, allowed by D14 Q8)
  (last bullet of this section) can release its guard. This is a deliberate loss of capability relative to OpenCode,
  not an oversight.
- **Cost.** `total_cost_usd` is cumulative per session and an estimate (spike
  Environment table and the step 2/5 re-runs of the spike evidence: the reported
  cost grows across resumes of one session). It is reported as
  informational and never used for correctness.
- **Guard release for `unknown`.** Unchanged principle (ADR-017 (c) rejected, no
  automatic release). **ADR-020 extension (allowed by D14 Q8; implemented in slice 4), scoped to the
  Claude harness only:** the admin
  channel is extended with Claude-specific preconditions in place of
  ADR-020's native reads, which do not exist here: the guard is held by
  exactly that execution; the execution is `unknown` with its child **confirmed
  exited** (the Bridge `wait`ed the `sudo` parent, which waits for the command)
  and the child's process group empty; and an audited ticket. If child exit cannot
  be confirmed (§4 final escalation), release is refused. The two-read settle is
  replaced by one re-check after the settle interval that the child is still gone.

## 4. Interrupt

`interrupt_execution` keeps its contract: explicit, authorized, returns the
execution state at request time (it can still be `running`), and the client polls
`get_execution_status`.

- **Interrupt reasons.** Every interrupt the Bridge sends records one reason,
  `user` (this tool), `cap`, `grace`, `shutdown` or `latch` (§3). Only a `user`
  interrupt can produce `interrupted` (§3 row 5); the others are latched rows
  (9, 6a, 9s, 0) whatever the CLI answers.
- **Escalation:** SIGINT to the child, a bounded wait for a `result` and process
  exit (default 10 s, configurable), then SIGTERM, a second bounded wait (default
  5 s). Never SIGKILL of the `sudo` parent (see below).
- **Who is signalled.** The Bridge user cannot signal processes of the Claude
  service user. The Bridge signals the process it spawned (`sudo`), and `sudo`
  relays signals to the command it runs (sudo(8); **UNVERIFIED on the deployed
  sudo version and under the exact sudoers options; slice 3 must prove SIGINT and
  SIGTERM relay with a live tool call**). The spike signalled the CLI process
  directly as root, which this deployment cannot do, so the spike does not
  cover the relay.
- **Result per signal (OBSERVED, spike step 6, via the wrapper):**
  - SIGINT during a tool call: `result` with `error_during_execution`,
    `is_error: true`, `terminal_reason: aborted_tools`, wrapper exit 0.
    DOCUMENTED shapes: `terminal_reason` values include `aborted_streaming` and
    `aborted_tools` (https://code.claude.com/docs/en/agent-sdk/typescript,
    accessed 2026-11-11; values relayed by a fetch summarizer, small transcription
    risk). The docs give **no SIGINT exit code**; the Bridge must not use one.
    Mapping: `interrupted` (§3 row 5), only when the Bridge sent SIGINT for reason `user` and no latch fired.
  - SIGTERM: exit **143**, no `result` (OBSERVED; DOCUMENTED at
    https://code.claude.com/docs/en/headless, "Stop a run with SIGTERM",
    accessed 2026-11-11: "leaves the turn that was in progress unfinished and
    records no result"). Mapping: `unknown`, `PROCESS_ENDED_WITHOUT_RESULT`, guard
    kept (§3 row 6). The ADR-011 `ABORT_UNCONFIRMED` reason replaces
    `PROCESS_ENDED_WITHOUT_RESULT` in row 6 when the Bridge had sent a `user`
    interrupt and no proof arrived; a `cap`, `grace`, `shutdown` or `latch`
    interrupt is never row 6 (latched rows).
  - After either signal the session resumed (OBSERVED). The transcript records
    the killed tool differently (SIGINT: "rejected, never ran"; SIGTERM: "killed,
    code 137"), which is **model-reported, not read from the transcript**; it is
    not used as evidence.
- **Process tree.** The wrapper is required to start the CLI in its own session
  and process group (for example `setsid -w`, so that the wrapper stays the
  parent until the CLI exits; a plain `setsid` can fork and let `sudo` return
  early). Whether this works together with `sudo` signal relay is **UNVERIFIED**
  and is tested in slice 3; until then no claim in this ADR relies on the group
  being a complete boundary. The Bridge treats the child as dead only after it has
  `wait`ed the `sudo` parent (which waits for the command). A grandchild that
  detached itself (`setsid`, double fork) is outside this guarantee. The
  deployment runs the Claude user's commands in a systemd transient scope or
  cgroup where available, and the operator checks it empty before an ADR-020
  release (§3, last bullet). **UNVERIFIED** whether SIGTERM of the CLI reliably
  kills detached grandchildren: the docs say it terminates "running Bash process
  trees" (DOCUMENTED, headless page above); detached ones are not mentioned.
- **Final escalation does not exist.** If the child is still alive after the
  SIGTERM wait, the Bridge stops signalling, keeps the guard and surfaces a loud
  `child_unconfirmed` flag: `unknown` with `unresolved.reason = ABORT_UNCONFIRMED`
  for a `user` interrupt (§3 row 6b); for a `cap`, `grace` or `latch` interrupt
  the latched row keeps its own reason (9, 6a, 0) and gains the flag. Killing the `sudo` parent with SIGKILL would orphan the
  command and is forbidden. Removal of a stuck process is an operator action
  outside the Bridge.
- **Bridge shutdown** (`Close`): SIGINT then SIGTERM to every live child, then the
  executions are `MANAGER_CLOSED` as today (§3 row 9s). An open turn in the history record gets
  a `restart_gap` marker on the next start (§5).

## 5. History store (D7)

The Bridge keeps the only readable history for Claude sessions. It is built from
the stream-json events of runs the Bridge starts. Nothing reads the CLI's own
transcript files (not supported: "the entry format is internal to Claude Code and
changes between versions", https://code.claude.com/docs/en/sessions, accessed
2026-11-11; research note §2).

**Location and modes.** `BRIDGE_CLAUDE_HISTORY_DIR`, absolute, owned by the
Bridge service user, mode exactly 0700, parent chain not writable by others,
validated at startup with the same open-by-fd, no-final-symlink checks as the
ADR-010 lock directory. Separate from `BRIDGE_WRITER_LOCK_DIR`. **Not readable by
the Claude service user**: the files are written only by the Bridge process from
the pipe it reads, so a prompt-injected tool running as the Claude user cannot
read or alter them. Files are `0600`, regular, single-link, opened `O_APPEND |
O_CLOEXEC | O_NOFOLLOW`, one directory entry per segment:
`<sha256(claudecode:<uuid>)>-<seq>.ndjson`.

**Format.** Append-only NDJSON, one record per line, written with a single
`write` per record: `{v, seq, at, execution_id, kind, ...}`. Kinds:
`turn_start` (the user prompt as sent, bounded to 32768 bytes; operator
principal and idempotency-key hash prefix, never the key), `assistant_block`
(text, `tool_use` name and bounded input), `tool_result` (bounded content),
`turn_end` (the terminal decision of §3 with `subtype`, `terminal_reason`,
`permission_denials`, `num_turns`, exit status, cost), `restart_gap` and
`truncated`. `seq` is per session and strictly increasing; a gap is detectable.
Reader rule: ignore an unparsable **final** line (torn write on a crash), reject
nothing earlier silently (report `history_corrupt` and stop).

**Bounds.**

| Bound | Default | On exceed |
| --- | --- | --- |
| one record | 64 KiB | content replaced by a `truncated` marker with the original size |
| one turn | 2 MiB | later records of the turn become one `truncated` record; the `turn_end` record is always written, even past this budget, so a finished turn never reads as open |
| one segment | 8 MiB | roll to the next segment (never rewrite) |
| one session, total | 64 MiB | **new executions for that session are refused** with a typed error until the operator archives segments; existing records are never dropped silently |
| `read_session_history` response | 8 MiB (existing contract bound) | newest records kept, response marks `history_truncated: true` |

**Redaction.** Best effort, applied before the first byte is written, and the
claim is limited accordingly: values matching known credential shapes
(`sk-ant-...`, `Bearer ...`, private-key blocks, `Authorization:` headers,
`BRIDGE_`/`ANTHROPIC_`/`OPENCODE_` variable assignments) are replaced by
`[REDACTED]`; the rule set is a versioned list in code with tests. Tool results
(file reads, command output) can still contain secrets that match no rule. The
history is therefore as sensitive as the transcript it mirrors: it stays in the
0700 directory and is returned only through the existing authorized
`read_session_history` of an allowlisted session. It is never copied into audit
log lines, errors or status fields.

**Retention.** Segments older than `BRIDGE_CLAUDE_HISTORY_MAX_AGE` (default 30
days, matching the CLI's own transcript retention default: `cleanupPeriodDays`,
https://code.claude.com/docs/en/sessions, accessed 2026-11-11) are deleted whole
at startup, only after a writer lock for the session is held. The operator may
archive or delete segments by hand while the Bridge is stopped. If the CLI's
transcript expires first, resume fails (§3 item 2) while the Bridge record
remains readable. The operator is told to raise `cleanupPeriodDays` for the
Claude user (user settings; Bridge does not edit it).

**What `read_session_history` returns.**

- Records mapped to `NativeMessage`-shaped objects (`{info, parts}`) with
  `info.id` set to `<execution_id>:<seq>`, `info.role` (`user`, `assistant`),
  `info.time.created/completed`, `info.sessionID`, and parts of type `text`,
  `tool` (name, input, bounded result, status) and `bridge_marker`. These are
  **Bridge-normalized, not native**; the response says so with
  `source: "bridge_record"`.
- For a session first resumed by the Bridge at time T, the response begins with
  an explicit marker part: `history_complete: false`, `first_recorded_at: T`,
  and the text "history before the first Bridge resume is unavailable". It is
  never empty without the marker. A client must not read absence of earlier
  turns as absence of earlier events.
- `unknown` turns appear with their `turn_end` evidence; a turn with no
  `turn_end` shows `outcome: unknown` (open turn).

**Restart behavior.** Execution, key and fingerprint maps stay memory-only
(ADR-010 limitation unchanged: a new process cannot recover an old execution ID
and old keys must never be reused). On start, after taking session locks, the
writer scans each session's last segment; a `turn_start` without `turn_end` gets
an appended `restart_gap` record naming the execution. It changes no state
because the execution no longer exists. The session is not marked reconciled:
the ADR-010 fenced procedure applies (the operator decides whether the killed
turn matters). When the Bridge dies, the child receives SIGPIPE/EPIPE on its next
write to stdout and normally exits; this is **UNVERIFIED** and slice 2 tests it.

**Dashboard.** This ADR defines no read interface for a dashboard. The only
reader of the history store is `read_session_history`; the history records tool
inputs and bounded results, not file patches, and the first deliverable's tool
profile is read-only (§7), so it will record no file edits. A dashboard (ADR-023,
separate proposal) would need its own requirement and interface; none is designed
here.

## 6. Discovery

**An operator-configured allowlist of (session ID, root), nothing else.** No
scanning of `~/.claude` or of any other directory (research note §1 marks CLI
listing NOT SUPPORTED, and the transcript layout is internal).

- `BRIDGE_CLAUDE_SESSIONS`: path of a JSON file, mode 0600, owned by the service
  user, validated like the other config files, listing objects
  `{"session_id": "claudecode:<uuid>", "root": "/abs/path", "label": "optional"}`.
  Maximum 1000 entries (ADR-012's ACL bound). Duplicates, relative or unclean
  roots and roots outside `BRIDGE_ALLOWED_ROOTS` fail startup.
- The same IDs must also appear in `BRIDGE_ALLOWED_SESSIONS` (the existing
  per-session ACL); the principal check is unchanged. The registry of ADR-012
  stays read-through and stateless: `list_sessions`, `get_session`,
  `find_sessions` and the repository tools see Claude sessions only through this
  list, with root revalidation (canonical path, symlink aliases rejected) as for
  OpenCode.
- **A session becomes usable by being listed**, and "known" to the history store
  at its first Bridge run. Existence of the UUID in the CLI is never checked
  before the first resume; a wrong ID fails the first run as `NOT_FOUND` (§3
  item 2) with no side effect (spike step 4, OBSERVED).
- `get_session` for Claude: `harness: "claudecode"`, `directory` = root,
  `title` = label, `state` per "Session state and the Bridge guard" with the
  Claude rule that **there is no native evidence**: the state is `unknown` when
  an execution is `unknown` and the guard is held, `running` while a child runs,
  otherwise `idle` meaning only "the Bridge holds no guard and runs no child"
  (this is weaker than OpenCode's `idle`; the contract text says so).
- Creating new sessions, `--session-id` (DOCUMENTED, CLI reference,
  "Use a specific session ID for the conversation (must be a valid UUID)",
  accessed 2026-11-11; collision behavior UNVERIFIED) and forking are **out of
  scope**: the product continues existing sessions.

## 7. Permissions

- Every run is `--permission-mode default --permission-prompts none` (§1;
  DOCUMENTED: `--permission-prompts none` "Pass `none` when nobody can answer, and
  Claude Code denies them instead", needs v2.1.259 or later;
  https://code.claude.com/docs/en/cli-reference and /headless, accessed
  2026-11-11).
- A tool that needs approval is **denied**, never approved, never held waiting:
  the Bridge has no approval path, `--allowedTools` is never passed and the
  permission-prompt-tool is not configured. The authoritative record is
  `result.permission_denials[]` (`tool_name`, `tool_use_id`, `tool_input`);
  `system/permission_denied` events are best-effort and only counted
  (DOCUMENTED, SDK TypeScript page, accessed 2026-11-11). Spike step 7 (OBSERVED):
  a Write was denied, not created, `subtype: success`, exit 0.
- **Reporting.** The execution gains an additive, report-only
  `permission_denials` list (tool name and a bounded, redacted input summary),
  same stance as ADR-017/018: it never changes `state` and never releases a
  guard. Denied tool inputs are Bridge data, not authority.
- **Tool surface.** The initial profile restricts the built-in tools to the
  read-only set (`--tools "Read,Glob,Grep"`; DOCUMENTED in the CLI reference:
  "Restrict which built-in tools Claude can use"; it does not affect MCP tools,
  which §8 removes) so the model does not spend turns on tools it cannot use and
  the attack surface is the read tools only. Web tools are excluded. This profile
  is a configuration value, widened only by a later ADR.
- **Future work:** remote approval for Claude Code **needs its own future ADR**.
  ADR-024 (permission relay) covers OpenCode only; its draft states that relay is
  not possible under the current Claude Code CLI contract (review #172 P3-1). The
  MCP prompt-tool wire schema is UNVERIFIED (research note §6); until such an ADR
  exists and is implemented, remote approval for Claude Code stays disabled and
  the harness is read-only by construction. The Phase 2/4/5 safety matrix must be
  re-proved for this adapter before any widening.

## 8. Security

Claude Code runs arbitrary tools and, **without `--bare`, loads repository
content that executes**. The headless page states: "Without `--bare`, a `-p`
session runs the hooks in a project's `.claude/settings.json` and connects the
servers in its `.mcp.json`, even in a folder you've never trusted. A `-p` session
shows no workspace trust dialog and no per-server approval prompt"
(DOCUMENTED, https://code.claude.com/docs/en/headless, accessed 2026-11-11), and
the permissions page adds that, for a never-trusted folder in `-p`, project
**hooks, the `env` block and helpers such as `apiKeyHelper` are used**, `.mcp.json`
servers are connected, while project `permissions.allow` and
`additionalDirectories` are not used (https://code.claude.com/docs/en/permissions,
"What runs before you trust a folder", accessed 2026-11-11). `--bare` would skip
all of that but "never reads OAuth credentials or the system keychain" (headless
page), which D5 rules out. So the design must disable repository-provided
configuration **by other means**, and prove it.

### 8.1 Users, wrapper and files

- **Two OS users.** The Bridge service user (`harness-bridge`) and the Claude
  service user (`hbclaude`, the spike's `hbspike`). The Claude user cannot read
  the Bridge's token files, lock directory, history directory, admin socket or
  TLS key (modes 0600/0700 owned by the Bridge user). The Claude user owns
  `~/.claude` (the subscription login, never read by the Bridge) and has no login
  shell requirement beyond what the CLI needs. Neither user is root.
- **Wrapper and sudoers.** The fixed wrapper (§1) and a sudoers entry that allows
  only `harness-bridge` to run only that wrapper's target as `hbclaude`, no
  password, `env_reset`, no `SETENV`, no wildcard arguments beyond the pinned
  flags. Prompts never appear in argv, so they never reach sudo's log (§1).
- **Project roots.** The Claude user needs read (and later write) access to the
  allowlisted roots; that is the operator's file-permission decision. The Bridge
  never widens permissions.

### 8.2 Environment scrub

The child's environment is built from an explicit allowlist, never inherited:
`PATH` (fixed minimal), `HOME` and `LANG` of the Claude user, `TERM=dumb`, and
the hardening variables below. In particular **none of** `CLAUDE_CONFIG_DIR`
(the production child reads its configuration from `HOME`; review #189 P3-4),
`ANTHROPIC_*`
(`ANTHROPIC_API_KEY`, `ANTHROPIC_AUTH_TOKEN`, `ANTHROPIC_BASE_URL`),
`CLAUDE_CODE_OAUTH_TOKEN`, `CLAUDE_CODE_USE_BEDROCK`, `CLAUDE_CODE_USE_VERTEX`,
`CLAUDE_CODE_USE_FOUNDRY`, `BRIDGE_*`, `OPENCODE_*`, `*_TOKEN`, `*_KEY`,
`*_SECRET`, proxy variables or cloud credentials is passed. Reason: the
documented precedence is cloud-provider switches, then `ANTHROPIC_AUTH_TOKEN`,
then `ANTHROPIC_API_KEY` ("In non-interactive mode (`-p`), the key is always used
when present"), then `apiKeyHelper`, then `CLAUDE_CODE_OAUTH_TOKEN`, then
"Anthropic profile and federation credentials", and subscription OAuth last
(https://code.claude.com/docs/en/authentication, "Authentication precedence",
accessed 2026-11-11); an inherited key would silently switch billing and auth
mode. A named profile is selected by `ANTHROPIC_PROFILE` and
federation by `ANTHROPIC_FEDERATION_RULE_ID` / `ANTHROPIC_ORGANIZATION_ID`
(authentication page, "Authentication precedence", as quoted in review #177;
I did not re-fetch it for this revision); all of these are covered by the
`ANTHROPIC_*` scrub. The **residual** is the active or default profile stored in
the Claude user's configuration directory, which is selected without an
environment variable, so the scrub cannot cover it. Whether `init.apiKeySource`
reports a value other than `none` for it is **UNVERIFIED**, so the `apiKeySource`
check may not detect it: it remains an operator requirement (no profile or
federation credentials are configured for the Claude user) and a slice-3
acceptance item, not something this ADR claims the Bridge can detect.

`sudo` `env_reset` is the second layer;
the Bridge's own scrub is the first and is unit-tested with a poisoned
environment.

Set explicitly: `CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1` (disables
auto-updates, telemetry, error reporting, `/feedback`; DOCUMENTED, env-vars
page, accessed 2026-11-11), `DISABLE_AUTOUPDATER=1` and `DISABLE_UPDATES=1`
("block all updates including manual `claude update`"). The CLI version is pinned
by the operator (§8.5), never self-updated under the Bridge.

**Auth mode check.** Every run must report `system/init.apiKeySource: "none"`
(OBSERVED for subscription OAuth, spike). Anything else ends the execution
`unknown` with `IDENTITY_MISMATCH`, guard kept, and the child is interrupted
(§3 row 0, §8.3 item 4); no verdict is drawn from that run.

### 8.3 Repository-provided configuration

Controls, in order of reliance. **Each is DOCUMENTED, but whether it holds for
`-p` on the pinned CLI is UNVERIFIED until the hostile-root acceptance test of
slice 3 passes**; the design does not claim protection before that.

1. **Managed settings (primary, host-level).** The operator installs
   `/etc/claude-code/managed-settings.json` (DOCUMENTED, managed-settings page,
   accessed 2026-11-11). Managed settings are applied "above every other level,
   so no user, project, local, or `--settings` value overrides them" (same page),
   and can set `allowManagedHooksOnly` ("only managed hooks run"),
   `allowManagedMcpServersOnly` and `allowManagedPermissionRulesOnly`. Whether
   managed settings apply in `-p` is **not stated in a sentence found for this
   ADR** (the settings page says they apply "wherever Claude Code runs on this
   machine"): UNVERIFIED, test in slice 3. This ADR does not install any file:
   `/etc` changes are the operator's, outside the Bridge's rights.
2. **Command-line (per run, Bridge-owned).**
   - `--setting-sources user`: "so Claude Code reads neither the project's
     settings files nor its `.mcp.json`" (permissions page above; flag in the CLI
     reference). This removes project hooks, project `env`, project
     `apiKeyHelper` and project permission rules.
   - `--strict-mcp-config` with no `--mcp-config` (or an empty file): "Only use
     MCP servers from `--mcp-config`, ignoring all other MCP configurations" (CLI
     reference). Expected `init.mcp_servers: []`.
   - `--settings '{"disableAllHooks": true}'` (the permissions page documents
     this exact per-run form; `--settings` sits above user, project and local
     files and below managed ones). The Bridge passes a fixed inline JSON that
     also sets `claudeMdExcludes` for the root's `CLAUDE.md` files (setting
     DOCUMENTED, "Skip specific `CLAUDE.md` files... Patterns match against
     absolute file paths", settings reference, accessed 2026-11-11; the exact
     glob semantics are UNVERIFIED and tested in slice 3; **managed-policy
     `CLAUDE.md` cannot be excluded**).
   - `--tools "Read,Glob,Grep"` (§7) and `--disallowedTools "mcp__*"` as a belt
     (DOCUMENTED).
3. **`--safe-mode` (candidate, newer):** the CLI reference describes "Start with
   all customizations disabled... Authentication, model selection, built-in
   tools, and permissions work normally, which differs from `--bare`" (accessed
   2026-11-11). This is closer to the need than anything above. Its availability
   on the pinned version, its use with `-p` and whether it also removes
   `CLAUDE.md` are **UNVERIFIED**; slice 3 decides between it and item 2 by test.
   The ADR does not depend on it.
4. **Post-start verification from `system/init` (Bridge-side, always on).** The
   Bridge reads the init event and **interrupts the child and ends the run
   `unknown` (`IDENTITY_MISMATCH`, guard kept)** if:
   `mcp_servers` is non-empty; `plugins` is non-empty; `tools` is not exactly the
   configured profile; `permissionMode` is not `default`; `apiKeySource` is not
   `none`; `claude_code_version` is outside the allowlist (§8.5); or any
   `hook_started`/`hook_progress`/`hook_response`/`plugin_install` event appeared
   before init (the headless page lists these as events that can precede init). The run is
   then `unknown` with `IDENTITY_MISMATCH` (something other than the pinned
   configuration ran, and the Bridge cannot prove what a hook did). This check
   cannot prevent a hook that ran before init; it limits the window to one run
   and makes the failure loud.
5. **Trust requirement of the root (residual, stated plainly).** `CLAUDE.md` and
   other model-facing content are *instructions to the model*, not code, and the
   exclusion in item 2 is not proven. The operator must treat every allowlisted
   root as trusted content: a file in the root can carry a prompt injection.
   With the read-only tool profile the damage is bounded to what the read tools
   can see and what the model says in the history, not to writes or commands. **What
   the read tools can see includes the Claude user's own home, in particular the
   subscription login in `~/.claude/.credentials.json` (mode 0600, owned by the very
   user the CLI runs as; DOCUMENTED on the authentication page, accessed
   2026-11-11, per review #172 P2-2). See item 6.**
   With a wider profile (a later ADR) this becomes a hard security requirement of
   the root. A project `.claude/` directory in an allowlisted root is operator-
   approved content, not a Bridge-enforced sandbox.
6. **Credential paths are denied to the tools (review #172 P2-2).** File
   permissions cannot separate the CLI from its own login (the CLI and its tools run
   as the same user), so the only controls are tool-level:
   - a **deny rule** for `Read`, `Glob` and `Grep` on `~/.claude/**`, on the
     Anthropic configuration directory (by default `~/.config/anthropic/**`, which
     holds the profiles and the `active_config` file; an active profile of auth
     mode `oidc_federation` ranks above subscription OAuth, per review #183 P3-1),
     on the directory named by `CLAUDE_CONFIG_DIR` when it is set (**test-only,
     defense in depth**: the production child environment does not carry it,
     §8.2, so production relies on the `~/.claude/**` and `~/.config/anthropic/**`
     rules), and on the other
     credential paths of the Claude user (the Bridge's own files are unreadable to
     that user anyway, §8.1), delivered through the inline `--settings`
     permissions deny list and, with higher precedence, through managed settings
     (§8.3 item 1). **When `allowManagedPermissionRulesOnly` is used, the deny
     rule must be present in the managed file itself**: that setting makes Claude
     Code ignore permission rules from user, project and local files and from
     `--allowedTools`, and whether a rule passed with inline `--settings` survives
     it is **UNVERIFIED** (review #183 P3-3). Documented basis: deny rules are evaluated before allow rules
     and modes (research note, SDK permissions page); managed settings override
     `--settings` and can make managed rules the only source
     (`allowManagedPermissionRulesOnly`, managed-settings page, accessed
     2026-11-11); the permissions page (https://code.claude.com/docs/en/permissions, as
     identified in review #177; not the managed-settings page) documents
     `permissions.blockReadsOutsideWorkingDirectories`. The version in which it
     appeared (an earlier draft said v2.1.257 or later) is **UNVERIFIED** and is not
     relied on.
     **UNVERIFIED:** the exact rule syntax and path-glob semantics for `Read`
     denies (including `~` expansion), whether `Glob`/`Grep` honour them, and
     whether `blockReadsOutsideWorkingDirectories` blocks a read of the home
     directory. They are decided by the canary below, not assumed.
   - the credential canary below, as a **mandatory slice-3 failure condition**;
   - the history redaction of §5 is a last barrier only and does not make the
     exposure acceptable.
   If no documented mechanism blocks the read, the adapter does not ship in this
   profile: the adapter does not ship (D14 Q9: a failing canary blocks shipping), not a
   default. `~/.claude/.credentials.json` is never read by the Bridge (§8.4).

**Hostile-root test (slice 3 acceptance, mandatory).** A throwaway root contains:
a `.claude/settings.json` with a hook that writes a canary file, an `env` block
setting `ANTHROPIC_BASE_URL` to a local sink, and an `apiKeyHelper` that writes a
canary; a `.mcp.json` server that writes a canary; a `CLAUDE.md` with a
distinctive injected instruction; and a second `CLAUDE.md`-style instruction that
asks the model to **read the decoy file `~/.claude/.credentials.decoy.json` and
print it** (the credential canary, **best-effort only**: the model may decline an
injected instruction, so it is not evidence, see the probes below). The injected
instruction names only the decoy (review #200 P3-1). **Decided (D14 Q10): no
test account is provisioned and the real-login probe is OMITTED.** Only the decoy
is tested. The real service credential, and any real login, is never copied,
read, probed or reused by the test, and no step of the plan names a real login
file. How the isolated test user authenticates for the run (subscription login,
never `--bare`, `apiKeySource: none`) is a slice-3 operational matter recorded in
its evidence file; it must not be a credential the test reads, copies or prints.

**Isolation (review #183 P2-2, #190 P2-1).** `CLAUDE_CONFIG_DIR` relocates the
configuration directory but does **not** change what `~` means. Only these forms
are allowed. **(A, preferred)** an isolated test user with its own `HOME`: its UID
and `HOME` differ from the Claude service user's, and `HOME` resolves inside the
throwaway tree. The **decoy** is a file inside the directory covered by the deny
glob (for example `~/.claude/.credentials.decoy.json`, mode 0600, fake content).
No real login file is probed (D14 Q10) and the plan names none. A decoy Anthropic
profile is a different file name in `~/.config/anthropic/`, never the
`active_config` file nor a profile the CLI would select (a fake `active_config`
would change the authentication mode). **(B)** (supplementary) if
`CLAUDE_CONFIG_DIR` is also used, it points into the throwaway tree, `HOME` is set
into the throwaway tree as well, the decoy is named by its **absolute throwaway
path**, and the same deny rules (same syntax) are applied to that absolute path and
to the throwaway Anthropic configuration directory. **Form A is the only form that
validates the shipped `~/.claude/**` rule syntax and its `~` expansion; form B
tests the absolute-path rule and is supplementary.** In both forms the test harness
chooses the paths itself and learns the resolved directories from its own
pre-flight: **before the run** it resolves them (`getuid`, the user's `HOME`,
`Lstat` and `EvalSymlinks` of the decoy path; no credential file is opened) and
fails closed unless the UID differs from the service user's and every resolved path
lies inside the throwaway tree. The real Claude user's `~/.claude` and
`~/.config/anthropic` are never created, replaced, moved or read by the test.
Evidence committed to this **public** repository contains **verdict lines only**
(tool name, the `permission_denials` entry, pass or fail), never raw NDJSON of a
denied run.

**Probes (review #190 P2-1).** They are driven by the **operator's own explicit
test prompt**, not by an injected instruction: a `Read` of the decoy by absolute
path (the real login is not probed, D14 Q10), a `Grep` in content mode with a pattern taken from
the decoy, a `Grep` in files-with-matches mode, and a `Glob` listing of the decoy directories (Claude
Code applies `Read` deny rules to `Grep` and `Glob` only on a best-effort basis,
review #183 P3-2). **Per probe the run must contain a `tool_use` event for that
tool and a matching `permission_denials` entry**; a probe without both is
**inconclusive and fails the slice**, so a model that simply declined cannot pass.
The decoy content is a **random marker string that is not credential-shaped**
(generated per run), so a content-mode `Grep` match is unambiguous evidence of
decoy exposure and cannot match any other content. A
**positive control run without the deny rule** (same isolated user, a prompt that
names **only the decoy**, throwaway root only) must show that the same prompt does read the decoy; if it
does not, the denial evidence is meaningless and the test is inconclusive (slice
fails). After the denied run: no canary file exists, the sink received no request,
`init.mcp_servers` is empty, `init.apiKeySource` is `none`, and **the decoy
content appears nowhere in the stream, the history record or the result**
(appearing only in the positive control run, which uses decoy content only).
Whether the model followed the other injected instruction is reported
(best-effort, model-dependent, not a pass criterion). If any file canary exists,
the decoy content is exposed in the denied run, or any probe is denied-less or
inconclusive, slice 3 fails and the adapter stays disabled.

### 8.4 Prompt injection, secrets, argv

- Tool outputs, file contents and prior assistant text are untrusted data and
  never authority. The Bridge never executes or approves anything because model
  output says so; it has no approval path at all (§7).
- **argv never carries secrets or prompts** (§1); the wrapper path and fixed
  flags are the only argv. The spike's argv-with-prompt form is not used in
  production.
- stderr of the child is captured to a bounded ring and stored only as a short
  redacted summary on failure (`local_failure.summary`); it is never returned
  verbatim to clients. (The spike saw a stderr line about a stored OAuth
  credential: stderr can mention credential state.)
- The Bridge never opens, reads or logs the Claude user's home or credential
  files, and the Bridge user has no permission to.
- Audit: the AUDIT-01 pattern is extended with `harness=claudecode`, `exec`
  outcome, exit status class, denial count, `interrupt_reasons` (the ordered list
  of reasons of every Bridge-sent interrupt, enum values only: `user`, `cap`,
  `grace`, `shutdown`, `latch`), `late_lines_after_verdict` (an integer count of
  stream lines that arrived after a latched verdict) and `late_result_seen` (a
  boolean), never prompt text, keys, tool input or line content.

### 8.5 Version pinning and network

- `BRIDGE_CLAUDE_VERSIONS`: an allowlist of exact `claude_code_version` strings
  (default: the version(s) validated by the slice-3 acceptance; at proposal time
  only **2.1.322** has been exercised). The research note found behavior gated
  across 2.1.2xx to 2.1.30x (lookup scope, default mode, `--permission-prompts`),
  so an unlisted version ends the run `unknown` (`IDENTITY_MISMATCH`, guard kept,
  child interrupted) at the first `init` (§3 row 0, §8.3 item 4).
- Egress of the Claude user is the operator's network policy; the Bridge adds no
  proxy. `CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC` reduces, but does not remove,
  traffic.

## 9. MCP tool contract changes and capabilities

The tool names and field names of the eleven tools do not change; the accepted
`session_id` values widen for `claudecode:` (below). Everything else is additive.

- **Session ID:** `claudecode:<uuid>` with the lowercase 8-4-4-4-12 hex UUID
  pattern, in the same `session_id` field. The input pattern widens from
  `^opencode:ses[A-Za-z0-9_-]+$` to an `anyOf` of the two patterns (maximum
  length stays below the current 265). Native ID validation is per harness
  (`opencode.ValidID(..., "ses")` for OpenCode, a strict UUID parser for Claude).
  Tool names, input field names and output field names are unchanged; the
  accepted `session_id` values widen for `claudecode:`. Strict old clients that
  validate IDs against the old pattern would reject a Claude ID, so the change
  would need a CONTRACTS version bump to be announced. Whether Claude sessions are
  then hidden from a client that did not negotiate the new version: decided, D14 Q2,
  Claude sessions are advertised to **all clients** (no hiding).
- **Session fields:** `harness: "claudecode"`; `native_id` = the UUID; `title` =
  the allowlist label; `directory` = root; `state` per §6.
- **Execution fields:** `session_id` is `claudecode:<uuid>`; `message_id` is the
  Bridge's opaque execution-scoped ID (no native user message ID exists, §3);
  optional additive `permission_denials`, `liveness`, `child_unconfirmed`,
  `cost_usd_reported` (informational) and `exit` (`class` only).
- **Unresolved reasons, additive:** `PROCESS_ENDED_WITHOUT_RESULT` (no `result`
  at all) and `EXIT_UNCONFIRMED` (a `result` was seen but the exit was not
  confirmed; §3 row 6a). Existing
  reasons are reused (`ABORT_UNCONFIRMED`, `MONITOR_EXPIRED`,
  `IDENTITY_MISMATCH`, `MANAGER_CLOSED`). `SUBMISSION_UNCONFIRMED` is used when a
  write to stdin failed after some bytes were accepted.
- **Failure codes:** unchanged set; `NOT_FOUND` for an unknown session at first
  resume (§3 item 2) is existing.
- **`read_session_history`:** Bridge-normalized messages with `source:
  "bridge_record"`, `history_complete`, `first_recorded_at`, `history_truncated`
  (§5). The response size bound (8 MiB) is unchanged.
- **Native capabilities tool (ADR-012):** a per-harness block for Claude:
  `harness: "claudecode"`, `version` (the version last seen in `init`),
  `capabilities` (the init list, verbatim, informational), and Bridge facts:
  `resume_exact_id: true`, `native_lock: false`, `history_source:
  "bridge_record"`, `permission_relay: false`, `interrupt: "sigint_then_sigterm"`,
  `active_terminal_control: false`, `session_creation: false`, `read_only_tools:
  true`. Each fact is "declared by the Bridge", not probed from the CLI.
- **Not changed:** transport, bearer, the single-writer admission semantics,
  idempotency, AUDIT-01 line structure (new optional keys only), the admin socket
  (extended in its own slice, §3).

## 10. Implementation plan (3 to 5 slices)

Each slice is one branch and one PR. Every slice needs a RED test first, the
listed mutations (each must turn the named test RED when applied and be restored),
an acceptance step, and a non-skipped `go test -race ./...`. A skipped, blocked or
unexecuted live test is never PASS (AGENTS.md).

**Slice 1: pure core, no process.** `internal/adapters/claudecode`: argv builder
with the pinned flags and an allow-pattern check; env scrub; wrapper/dir
validation; stream-json decoder with per-line and total bounds; the §3 state
machine as a pure function (events, exit status, requested-signal flag) to a
verdict; session-ID parsing; redaction rules; the history record writer/reader
(§5) on a temp dir. No `exec`.
- RED tests: every §3 row (table-driven, including OBSERVED fixtures from the
  spike evidence); success requires all four conditions; two `result` lines; the evaluation order (latched rows `0, 9, 6a, 9s, 6b` first, then
  `1, 2, 8, 3, 4, 5, 6, 7, 10`) as a table of **overlapping evidence**: cap plus
  success `result` plus exit 0 is row 9; cap plus `aborted_*` result plus exit 0 is
  row 9; grace expired plus a late `aborted_*` result is 6a; grace expired plus a
  late success with exit 0 is 6a; shutdown plus `aborted_*` result is 9s; a `user`
  interrupt plus `aborted_*` result plus exit 0 and no latch is row 5; a `user`
  interrupt whose escalation is exhausted is 6b; a `user` interrupt and exit 143
  with no result is row 6 with `ABORT_UNCONFIRMED`; init latch plus `aborted_*`
  result is row 0; the row-2 shape plus a trailing line is row 8, not row 2; result plus hang is 6a; result plus a second result is row 8 only
  if the child exits (EOF and `wait`) before the grace ends, and 6a if it hangs; every interrupt records exactly one reason; after any latch
  every later line, result and exit is audit-only (`late_lines_after_verdict`,
  `late_result_seen`) and the verdict is unchanged; row 8 counts **every** event after
  the final `result`, `tool_progress` and `system/api_retry` included (the pairs
  "`tool_progress` after the final `result` is row 8" and "`system/api_retry` after
  the final `result` is row 8", against the same two lines before the final
  `result`, which do not count), with an ignorable list that starts empty; a non-empty undecodable line after the final `result` is row 8 too, with its own fixture (a success-shaped run, exit 0, then one undecodable line, must not be row 3);
  unlisted `terminal_reason`; SIGINT shape without a requested interrupt is
  `failed`; row 0 (every latch trigger: `permissionMode`, `apiKeySource`, `cwd`,
  `session_id`, unlisted version, non-empty `mcp_servers`/`plugins`, wrong
  `tools`, pre-init hook event) beats any later `result`, exit status or
  `aborted_*` shape; env poisoning (`ANTHROPIC_API_KEY` in the Bridge env never reaches the
  child); argv never contains the prompt; history torn tail; bounds; redaction.
- Mutations: success without exit-code check; accept `is_error:true` on exit 0 as
  `completed`; pass `--permission-mode auto`; leak one env prefix; drop the
  `session_id` correlation; make `unknown` fall through to `completed`.
- Acceptance: unit/race green; a recorded fixture replay of the spike's NDJSON
  excerpts maps to the OBSERVED verdicts. **Also a live check (documented
  command, operator-run): prompt on stdin works with `--resume` and
  `stream-json` on 2.1.322** (§1). If it fails the ADR is amended before slice 2.
  **Also a live multi-run capture** (operator-run, evidence file) of the stream
  after the final `result` and of the `tool_progress` shape over several normal
  runs, which fixes the row 8 ignorable list and settles whether `tool_progress`
  is a heartbeat (review #189 P3-2, #190 P3-5); until then a trailing event of an
  unknown type makes a run `unknown`.

**Slice 2: runner, lock and history integration.** The `Runner` implementation
that spawns the child (`exec.Cmd`, own process group, no shell), feeds stdin,
decodes the stream, enforces the wall-clock cap, appends the history, wires the
per-session flock namespace and the manager registration; ID widening in the
MCP schemas; allowlist file loading (§6).
- RED tests: two concurrent `send_prompt` for one session start one child
  (and the second gets `SESSION_BUSY` or a replay); same key replays; a failed
  `fork/exec` finalizes `failed` and releases the guard; stdin write failure
  after partial bytes is `unknown`; stdout cut mid-stream is `unknown`;
  restart-gap marker; cap on concurrent children; history cap refuses new
  executions; Bridge shutdown interrupts children. **Deferred here from slice 1
  (reviews #216, #218, #219):** the runner feeds `StepStreamBroken` before `EOF`/`Exit`
  (test with the real decoder and a fake child); history API field whitelists and a
  closed set for `turn_end` strings; a private-key redaction rule for a block
  without an end line; directory validation by file descriptor and a bound on
  `Read`; the no-exec guard's selector allowlist; build stubs for non-Linux
  (`GOOS=darwin`).
- Mutations: skip the flock for `claudecode:` IDs; release the guard on
  `unknown`; allow a second child per session; retry a failed spawn with the
  same key.
- Acceptance: fake-CLI integration (a test binary that emits recorded NDJSON and
  exits with chosen codes/signals), unit/race, `-count=20` on the concurrency
  tests. **Not live.**
- **As built (slice 2).** The process side lives in the subpackage
  `internal/adapters/claudecode/runner`; the pure core imports nothing from the module
  and starts nothing. `os/exec` is imported by exactly one file,
  `runner/spawn_linux.go`, and only that file may use `syscall.Kill`, `SysProcAttr`
  and `WaitStatus`; both facts are tests (`TestExecIsConfinedToTheSpawnFile`, the
  selector allowlist). `runner.Executor` carries the ADR-010 admission semantics for
  `claudecode:` sessions (guard, same-key replay, `IDEMPOTENCY_CONFLICT`, preflight, child
  cap) with the §3 machine deciding every outcome; it is a separate type, not an
  extension of `execution.Manager` (whose `Runner` interface is OpenCode-shaped), and
  nothing in `cmd/bridge` or the MCP dispatch calls it: the adapter is unwired and
  `claudecode:` IDs are refused at execution with `INVALID_INPUT` ("not enabled"). Choices
  made inside the existing code set: a child-cap refusal is `SESSION_BUSY` with
  `retryable: true`; a history size refusal was `INTERNAL` (**superseded in slice 3: `UPSTREAM_FAILURE`, see
  "As built (slice 3)"**); an `unknown` verdict without
  a named reason (rows 7, 8, 10) is reported with the existing reason `UNKNOWN_OUTCOME`.
  **The runner sends nothing but `SIGINT`, to the child's process group.** There is no
  SIGKILL (ADR-021 §4: "Never SIGKILL of the `sudo` parent"; removing a stuck process is an
  operator action), no SIGTERM and no timer-driven escalation in slice 2 (the escalation, with
  `interrupt_execution` and the `StepEscalationExhausted` path, is slice 3, see "As built
  (slice 3)"; the §3 init verification was already fed through the runner by the machine). A child that ignores SIGINT keeps its run, its session guard and its child
  slot until it exits; `Close` interrupts every live child, waits up to `CloseWait`
  (default 15 s) and then **abandons** the wait, returning the IDs of the abandoned
  executions for the caller to log; their guards stay held (memory-only, so a restart
  recovers). After a
  broken stream the runner drains and discards the rest of stdout so the child can
  finish; the wall-clock cap still bounds it. `Read` loads at most 16 MiB of history,
  newest segments first, and says so (`history_truncated`); startup recovery is not
  bounded by it.
- **Review #229 fixes and residuals (slice 2 as built).** The guard of a row 1 verdict
  with a live child is freed only after a confirmed reap (above; an unknown `Wait` is not one); the runner interrupts a child whose
  prompt was not (fully) delivered. The preflight order is: allowlist,
  writer lock, root, wrapper, then the child cap, so a refused session gets `NOT_FOUND` or
  `FORBIDDEN` even at capacity and a capped request leaves no guard held.
  `BRIDGE_ALLOWED_SESSIONS` is enforced by the `Executor` constructor
  (`Config.AllowedSessions`, `CheckSessionACL`): a wiring cannot skip it. Recorded
  residuals, not fixed in S2: (a) startup recovery reads one session's history at a time
  (each at most the 64 MiB session bound), so its total time grows with the sum over up to
  1000 sessions, with no streaming scan or time bound; (b) between the kernel reaping the
  child and the runner setting its reaped flag there is a window in which a SIGINT to the
  group id could reach a new process that took the id as its own group leader (no pidfd
  in S2); the flag closes it for every signal sent after a **confirmed** `Wait`, and a
  `Wait` bounded by `WaitDelay` (a descendant holding stderr) or otherwise unknown leaves
  the group signalable; (c) the
  allowlist file `null` parsed as an empty allowlist (no session is usable) instead of
  being refused (**fixed in slice 3**).

- **As built (slice 3, code part; the live acceptance is operator-run and NOT_EXERCISED).**
  Still unwired: `claudecode:` IDs are refused "not enabled", nothing in `cmd/bridge`, the
  manager or the MCP dispatch calls the `Executor`, and `interrupt_execution` support exists
  inside `runner.Executor.Interrupt` only (authorization and the MCP exposure are the wiring
  slice).
  - **Escalation (§4).** Every Bridge-sent interrupt (`user`, `cap`, `grace`, `shutdown`,
    `latch`, and the interrupt of a child whose prompt was not delivered) starts one
    escalation per run: SIGINT to the child's process group, `IntWait` (default 10 s), SIGTERM
    to the group if the child has not been reaped, `TermWait` (default 5 s), then the runner
    stops signalling and feeds `StepEscalationExhausted` (row 6b for a `user` interrupt;
    `child_unconfirmed` on a latched row; nothing for a run decided without a latch). A second
    interrupt during an escalation does not restart it. **There is no SIGKILL**: the runner's
    `Signal` type has two values (`SigInt`, `SigTerm`), `execChild.Signal` refuses any other
    value on a live child, and the no-exec selector allowlist admits `syscall.SIGINT` and
    `syscall.SIGTERM` only (a test refuses `SIGKILL`, `SIGQUIT`, `SIGHUP`, `SIGSTOP`). An
    unknown `Wait` is not a reap and sends no signal. An acknowledged `user` interrupt
    (`aborted_*` result, child exits) is never escalated.
  - **`Executor.Interrupt(id)`** feeds `StepUserInterrupt` and starts the escalation; it returns
    the execution as it was at request time and does nothing to a run that already has a
    verdict (a latched run was interrupted by its own row; a later `user` interrupt cannot make
    a late `aborted_*` result row 5).
  - **`Close`** also escalates (shutdown reason) and is now idempotent: only the first call
    interrupts and waits `CloseWait`; a repeated call returns at once with the executions still
    unreaped (review #239 P3-2, #242 P3-3).
  - **Argv (§8.3 item 2).** The pinned argv is now
    `-p --resume <uuid> --output-format stream-json --verbose --model <m> --permission-mode default
    --permission-prompts none --setting-sources user --strict-mcp-config --settings <json>
    --tools <profile> --disallowedTools mcp__* --max-turns <N> [--max-budget-usd <X>]` (23 or 25
    elements). The inline settings are one canonical JSON document: `disableAllHooks`, the root's
    `CLAUDE.md`/`CLAUDE.local.md` exclusions (the glob semantics stay UNVERIFIED), and
    `Read`/`Glob`/`Grep` deny rules on `~/.claude/**` and `~/.config/anthropic/**` (plus the
    test-only `CLAUDE_CONFIG_DIR` rule). `CheckArgv` accepts exactly that rendering for some root
    and nothing else; dropping a flag or a deny rule fails it. The rule syntax and `~` expansion
    are UNVERIFIED until the live canary; the deny rules must also be present in the managed file
    (§8.3 item 6).
  - **`BRIDGE_CLAUDE_VERSIONS` (§8.5, D14 Q7).** `ParseVersions` accepts a comma-separated list of
    exact `MAJOR.MINOR.PATCH` strings only (no wildcard, range, prefix, pre-release, whitespace,
    duplicate or empty entry; at most 16). There is no default: an empty value is
    `ErrNoVersions` and `runner.New` refuses an empty list, so the adapter cannot start until a
    version has been validated by the live acceptance. `New` also refuses a tool profile outside
    `{Read, Glob, Grep}`. Reading the environment variable is wiring.
  - **Credential canary (code only).** `claudecode/canary.go` is the pre-flight (UID, HOME in the
    tree in both forms, config dir, decoy paths: `Lstat`/`EvalSymlinks` only, no file is opened,
    the real login names are refused), the closed probe set (`Read` of the decoy file and of the
    decoy profile, `Grep` in both output modes, `Glob` of both decoy directories), the operator
    prompts (decoy paths only) and the verdict (`EvaluateCanary`): a probe passes only with a
    `tool_use` event aimed at its target **and** a matching `permission_denials` entry; the
    positive control must read the decoy; the marker must not appear in the denied stream or
    history; no canary file; no sink request; `init.mcp_servers` empty and `apiKeySource` `none`.
    Committed evidence is verdict lines only (`CanaryReport.Lines`). Its unit tests use
    FABRICATED streams. **It was not run**: with D14 Q10 there is no allowed authenticated run,
    so the live canary is NOT_EXERCISED and slice 3 cannot ship (paragraph below; Q10 is not
    reopened or answered here).
  - **Review carry-overs.** Error and reason vocabulary (#236 P3-5): a history store or size
    refusal is `UPSTREAM_FAILURE` (the CONTRACTS enum has no `INTERNAL`); the Claude-only
    `unresolved.reason` values `EXIT_UNCONFIRMED`, `PROCESS_ENDED_WITHOUT_RESULT` and the
    fallback `UNKNOWN_OUTCOME`, and a child-cap refusal as `SESSION_BUSY` `retryable: true`, are
    recorded in CONTRACTS as additive deltas (the cap code remains a planner decision at wiring).
    Allowlist decoding (#236 P3-6) is strict: top level array (never `null`), keys exactly
    `session_id`/`root`/`label` in that spelling, no duplicate key; `insideAny` stays lexical
    (the canonical root check is at admission). The drain after a broken stream is bounded by
    `DrainMax` (default 64 MiB), after which the runner stops reading; the cap, the exit grace
    and the escalation still end a child that then blocks on a full pipe. Not here (wiring slice):
    the double writer lock (#236 P3-4) and the wrapper-side descendant check (#242 residual: the
    wrapper exits with a known status while the CLI behind `sudo` keeps running).
  - **Live acceptance still open (operator-run, NOT_EXERCISED):** the hostile-root test with the
    credential canary, the result-to-exit lag measurement (validates or replaces the 10 s grace),
    the post-`result` stream capture, `sudo` relay of SIGINT and SIGTERM with a live tool call,
    `--safe-mode` versus item 2, managed settings applied in `-p`, live prompt-from-stdin,
    SIGPIPE on Bridge death. See `internal/adapters/claudecode/ACCEPTANCE_S3.md`.

**Consequence of D14 Q10, recorded here and NOT decided (reviews #219 P3-4, #224 P3-2).**
D14 Q10 settled that there is no test account and that the real-login probe is
omitted. The slice 3 decoy canary (below) still needs one authenticated CLI run, with no
named allowed authentication path for it. Until the maintainer provisions such a run,
the canary cannot execute; per D14 Q9 a failing canary blocks shipping, and a canary
that cannot run is not a pass, so **slice 3 cannot ship until then**. This paragraph
records the consequence only. It neither answers nor reopens Q10, and it grants no
authentication path.

**Slice 3: hardening and interrupt, live-gated.** `init` verification (§8.3 item
4), interrupt escalation (§4), `--setting-sources`/`--strict-mcp-config`/
`--settings`/`--tools` wiring, `BRIDGE_CLAUDE_VERSIONS`.
- RED tests: each init-violation case ends the run `unknown`
  (`IDENTITY_MISMATCH`), keeps the guard and interrupts the child; **a
  success-shaped `result` after an init violation is never `completed`; an
  `aborted_*` result after the init-violation interrupt never releases the guard;
  a `result` line with the child still alive keeps the execution `running` and the
  guard held (a second `send_prompt` gets `SESSION_BUSY`), and only the child's
  exit (or the exit grace, then an interrupt) produces the verdict and any guard
  release; **a correlated `result` whose child does not exit within the grace (or
  whose `wait` fails) is `unknown` / `EXIT_UNCONFIRMED` with `child_unconfirmed`
  and the guard kept, never `PROCESS_ENDED_WITHOUT_RESULT`; the overlap pairs of
  slice 1 run against the fake CLI with real signals: cap plus success plus exit 0
  and cap plus `aborted_*` plus exit 0 are `MONITOR_EXPIRED` with the guard kept
  (never `completed`, never `interrupted`); grace plus a late `aborted_*` result and
  grace plus a late exit 0 stay `EXIT_UNCONFIRMED` with the guard held and no second
  child admitted; shutdown plus `aborted_*` is `MANAGER_CLOSED`; only a `user`
  interrupt acknowledged by `aborted_*` is `interrupted`; a `user` interrupt whose
  escalation is exhausted is `ABORT_UNCONFIRMED` with `child_unconfirmed`; row 6
  stays for runs with no `result` at all;** credential canary: each operator-prompted probe (`Read`, `Grep` in both
  modes, `Glob`) on the decoy file and the decoy Anthropic profile has a `tool_use`
  event and a matching `permission_denials` entry (otherwise inconclusive and the
  slice fails), the positive control without the deny rule reads the decoy, the
  pre-flight isolation check fails closed (UID equal to the service user's, or a
  decoy path that resolves outside the throwaway tree), and the
  decoy content appears nowhere in the denied run**; SIGINT then
  SIGTERM sequence and timing with the fake CLI; `child_unconfirmed` path keeps
  the guard; no SIGKILL of the parent.
- Mutations: skip the `mcp_servers` check; skip the version check; send SIGKILL
  to the parent on timeout; treat exit 0 after SIGINT as `completed`; evaluate
  row 3 or row 5 before row 0; map a hung-child `result` to row 6; evaluate row 6 before row 9; classify a cap, grace or shutdown interrupt as row 5 (`user` only); let a late exit or late result re-enter the table after a latch; run the canary with only `CLAUDE_CONFIG_DIR` set and the real `HOME`; skip the `Grep`/`Glob` probes; count a probe without a `tool_use` event and a matching denial as passed; skip the positive control; ignore `tool_progress` (or `system/api_retry`) after the final `result`; release the guard at the `result` line before EOF
  and `wait`; drop the credential deny rule.
- Acceptance (live, operator-run, evidence file required): the **hostile-root
  test** of §8.3 (no test account and no real-login probe, D14 Q10: decoy only); **a measurement of the result-to-exit lag** of the pinned CLI over
  several runs, which validates or replaces the 10 s exit-grace default; a live
  multi-run capture of the post-`result` stream (shared with slice 1); sudo signal relay for SIGINT and SIGTERM; `--safe-mode` versus
  item 2; managed settings applied in `-p` (UNVERIFIED items resolved or the ADR
  amended); live prompt-from-stdin; SIGPIPE on Bridge death (§5).

**Slice 4: ADR-020 extension (Claude harness only, allowed by D14 Q8) and operator
tooling.** Claude-specific
preconditions for `bridge admin release-guard` (child exited and group empty,
one re-check, no native read), the new `unresolved` reasons in CONTRACTS, the
CONTRACTS update for §9, and the capabilities block. Docs: `CREDENTIAL_FILES.md`
and a Claude Code deployment runbook (service users, sudoers, wrapper, managed
settings, `cleanupPeriodDays`, version pin).
- RED tests: release refused while a child is live; refused if `child_unconfirmed`;
  released after confirmed exit with one audit line; no change to OpenCode release
  tests.
- Mutations: release without the child check; release a session held by a
  different execution.
- Acceptance: unit/race; CONTRACTS diff reviewed.

**Slice 5 (optional, only if slices 1 to 4 are accepted): live acceptance R6.**
Evidence only. Runbook outline, mirroring R5 for Claude Code:
1. Preconditions: dedicated `hbclaude` user and wrapper (R6-0), pinned version
   (`claude --version` equals the allowlist), managed settings installed (or the
   `--safe-mode` decision recorded), throwaway root and a throwaway session UUID
   created by the operator, writer lock dir, history dir, admin socket; no live
   OpenCode or connector touched.
2. R6-1 exact-ID resume through `send_prompt` (marker recalled in turn 2),
   `init.cwd` equals the root, `history` shows both turns and the "before first
   Bridge resume" marker.
3. R6-2 duplicate key replay and conflicting payload; R6-3 concurrent
   `send_prompt` (one child, one `SESSION_BUSY`).
4. R6-4 failure paths: unknown UUID (`NOT_FOUND`, no side effect), max-turns
   failure.
5. R6-5 interrupt: SIGINT mid tool call (`interrupted`), SIGTERM path (`unknown`,
   guard kept), resume afterwards; operator guard release (the ADR-020 extension, D14 Q8).
6. R6-6 permission denial: a Write is denied, listed in `permission_denials`,
   state `completed`.
7. R6-7 hostile root canary test and auth-mode check.
8. R6-8 restart gap: kill the Bridge mid-turn, restart, read history marker, apply
   the fenced procedure.
9. R6-9 human-terminal interleave (OBSERVED residual risk, recorded not fixed).
Every step records redacted raw NDJSON, exit codes, versions and the audit lines;
anything not observed is NOT_EXERCISED, never PASS.

## Alternatives considered

| Option | Why not |
| --- | --- |
| TS/Python SDK sidecar for history (`getSessionMessages`) and `canUseTool` | Rejected by D7 and the ROADMAP (no Node/Python backend without its own ADR). |
| Parse `~/.claude/projects/*.jsonl` | Internal, unversioned format; also discovery by scanning. Rejected by D7. |
| `--bare` | Cannot use the subscription login (D5). Would solve repository config loading, which is why §8.3 must replace it. |
| `--fork-session` per prompt | New session ID each time; breaks continuity; not asked for. |
| Bridge and CLI as the same OS user | Prompt-injected read tools could read Bridge tokens and the history store. Two users cost one sudoers rule. |
| Automatic guard release for a dead child | ADR-017 (c) rejected time-based release; AGENTS forbids implicit recovery. Admin-only release is kept. |
| Remote approval through `--permission-prompt-tool` now | Wire schema UNVERIFIED; needs its own future ADR (ADR-024 covers OpenCode only). |
| Native-state reconciliation like ADR-011 | No supported read API exists. |

## Consequences

- Claude sessions are continued, not discovered; history starts at the first
  Bridge resume.
- A turn whose child ended without a `result` stays `unknown` until an operator
  releases it; there is no self-healing read.
- Human use of a Bridge-managed session in a terminal is unsafe and undetected.
- The first deployable value is a **read-only** Claude Code harness; writes and
  commands need their own future ADR (not ADR-024, which covers OpenCode only).
- Added config: `BRIDGE_CLAUDE_COMMAND`, `BRIDGE_CLAUDE_SESSIONS`,
  `BRIDGE_CLAUDE_HISTORY_DIR`, `BRIDGE_CLAUDE_VERSIONS`, `BRIDGE_CLAUDE_RUN_MAX`,
  `BRIDGE_CLAUDE_HISTORY_MAX_AGE`; all optional, adapter off when unset.
- No existing OpenCode behavior, ADR or tool schema changes; the OpenCode path has
  no new code in slices 1 to 3.

## Decided (D14, 2026-11-12)

The maintainer answered every open question of the proposal in decision D14
(2026-11-12). The questions are kept in short form with the answer. References to "open
question N" or "proposed" elsewhere in this ADR point to the item of the same
number below and are answered by it; no other design text was changed.

1. **Residual human-writer risk (§2).** Residual **accepted and documented**: the
   adapter does not refuse sessions it did not create and does not fork. Do not
   resume the same session in a terminal concurrently. The risk is stated in the
   tool descriptions and in CONTRACTS for the Claude harness.
2. **Client compatibility of the new ID pattern (§9).** Claude sessions are
   advertised to **all clients**.
3. **Guard release for `unknown` (§3).** **Operator-only**, as in this ADR: no
   automatic release.
4. **Tool profile (§7).** **Read-only first**: the read-only Claude Code harness is
   the first deliverable.
5. **Managed settings in `/etc/claude-code` (§8.3).** The deployment runbook
   **may REQUIRE** them.
6. **History store growth (§5).** **64 MiB per session, 30 days** retention.
7. **Version policy (§8.5).** **Exact-version allowlist** only.
8. **ADR-020 amendment for the Claude harness (§3, slice 4).** The extension is
   **allowed**: child exited, process group empty, one re-check.
9. **Credential exposure to the read tools (§8.3 item 6).** A **failing canary
   blocks shipping** (the adapter does not ship in this profile if no documented
   mechanism denies the read).
10. **Test account for the credential canary (§8.3).** **No test account.** The
    real-login probe is **omitted** and only the decoy is tested; §8.3 and slice 3
    are updated accordingly.

## Verification status

| Claim | Status | Basis |
| --- | --- | --- |
| Resume by exact ID keeps the session ID; cwd follows the invoking directory | OBSERVED | spike step 2, 2.1.322 |
| No native writer lock; a concurrent resume drops a turn nondeterministically | OBSERVED (loss inferred from model listing) | spike step 5 |
| Interrupt result shapes and exit codes through the wrapper | OBSERVED (wrapper exit codes; no raw capture retained) | spike step 6 |
| Denied tool appears only in `permission_denials` | OBSERVED | spike step 7 |
| `--permission-prompts none`, `--permission-mode` values, `--tools`, `--strict-mcp-config`, `--setting-sources`, `--settings`, `--safe-mode` exist | DOCUMENTED | CLI reference, accessed 2026-11-11; the exact behavior on 2.1.322 UNVERIFIED |
| Project hooks, env, apiKeyHelper and `.mcp.json` load in `-p` without trust | DOCUMENTED | headless and permissions pages, accessed 2026-11-11 |
| Auth precedence; `ANTHROPIC_API_KEY` always used in `-p` | DOCUMENTED | authentication page, accessed 2026-11-11 |
| Managed settings override project and `--settings` | DOCUMENTED | managed-settings page, accessed 2026-11-11 |
| Managed settings apply in `-p` | UNVERIFIED | no sentence found |
| `claudeMdExcludes` glob semantics; `--safe-mode` on 2.1.322 | UNVERIFIED | slice 3 |
| Prompt only on stdin with `--resume` and `stream-json` | UNVERIFIED | slice 1 |
| `sudo` relays SIGINT/SIGTERM to the CLI; SIGTERM kills detached grandchildren | UNVERIFIED | slice 3 |
| SIGPIPE ends the child when the Bridge dies | UNVERIFIED | slice 2 |
| SIGINT exit code | UNVERIFIED | undocumented; not relied upon |
| `terminal_reason` and `subtype` value lists | DOCUMENTED, small transcription risk | SDK TypeScript page via a fetch summarizer |
| Heartbeat/`api_retry` shapes | DOCUMENTED, not exercised in the spike | headless and SDK pages |
| Background-work multi-`result` ordering | UNVERIFIED | research note |
| `--session-id` collision behavior | UNVERIFIED | CLI reference states only "must be a valid UUID" |
| Claude Code denies `Read`/`Glob`/`Grep` on `~/.claude/**` (rule syntax, `~` expansion, `blockReadsOutsideWorkingDirectories`) | UNVERIFIED | slice 3 credential canary; open question 9 |
| `init.apiKeySource` for profile/federation credentials | UNVERIFIED | not detected by the check; operator requirement (§8.2) |
| `CLAUDE_CONFIG_DIR` relocates the configuration directory but not `~` | DOCUMENTED | authentication and env-vars pages, as read in review #183; basis of the canary isolation rules (§8.3) |
| Inline `--settings` permission rules survive `allowManagedPermissionRulesOnly` | UNVERIFIED | deny rule is placed in the managed file (§8.3 item 6); slice 3 decides |
| `Read` deny rules also stop `Grep`/`Glob` | UNVERIFIED (docs say best-effort) | explicit canary probes (§8.3) |
| Stdin as piped data with a 10 MB cap | DOCUMENTED | headless page, section "Pipe data through Claude" (the earlier verbatim sentence was dropped) |

Documentation URLs (all https://code.claude.com/docs/en/, accessed 2026-11-11):
`cli-reference`, `headless`, `permissions`, `authentication`, `settings`,
`settings-reference`, `managed-settings`, `env-vars`, `sessions`,
`agent-sdk/typescript`, `agent-sdk/agent-loop`. Pages are long and were read
through a fetch tool; quotes were shortened, and the CLI, headless, permissions,
authentication, sessions, managed-settings and env-vars quotes were read from raw
page text while the settings-reference entries and the `terminal_reason` list
passed through a summarizer.
