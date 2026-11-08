# ADR-020: Guard recovery without a Bridge restart

Date: 2026-11-07. Status: **ACCEPTED, option (b)** (operator-only fenced
release).

**Acceptance note (maintainer, 2026-11-08, via Planning Desk, decision D2):**
option (b) is accepted and implemented as outlined in §4. Option (c) may follow
later as a separate amendment, after live evidence from (b). Option (d) is
rejected. Option (a), the restart, stays the fallback and the token-rotation
path (ADR-019). The #116 review nit N1 (lock wording of outline steps 6 and 8)
is folded into §4. The proposal text below is kept as written on 2026-11-07,
except for that wording and the status lines.

Numbering: `docs/adr` holds ADR-010 to ADR-014 and ADR-017 to ADR-019;
ADR-015 and ADR-016 are referenced in `docs/` without files and are treated as
reserved. No document references ADR-020, so it is the next free number.

Must preserve ADR-010 (single writer, memory-only records), ADR-011 (late
completion), ADR-017 option (a) and ADR-018 option (d), all accepted. Amends
nothing yet.

## Problem

When an execution ends `unknown`, this Bridge keeps the session's busy guard
until the process restarts (ADR-010). R5 Step 4, R5b (F-7) and F-6 each needed a
restart to send to the session again. A restart:

- loses every in-memory execution, idempotency and parent-ID record for **all**
  sessions (ADR-010 "Restart, retry and recovery limitation"), so releasing one
  guard weakens duplicate protection everywhere;
- needs host root (systemd) access and the full ADR-010 fenced procedure;
- is also the only revocation path for the connector token (ADR-019), which
  couples two unrelated operations.

## 1. Current mechanism (`main` `22264de`)

| Behavior | Location |
| --- | --- |
| Admission reserves the guard before any native I/O: `busy[session] != ""` returns `SESSION_BUSY`; otherwise `busy[session] = id`. | `internal/execution/manager.go:228-234` |
| A preflight rejection releases only its own reservation (`busy[session] == id`). | `manager.go:254-259` |
| Admission preflight re-reads native state: status (`busy`/`retry` → `SESSION_BUSY`), full history with the F-5 predicate, pending permission. | `manager.go:309-340`; predicate `internal/adapters/opencode/client.go:50-61` (used at `manager.go:323`) |
| A run that never invoked Prompt is finalized `failed` and releases its guard. | `manager.go:365-388` (release at `384-386`) |
| Monitor expiry or an unprovable outcome sets `unknown` (`unresolved`), which never releases the guard. | `internal/execution/reconcile.go:57-74`; `MONITOR_EXPIRED` at `reconcile.go:401` and `manager.go:458` |
| The only later release is correlated terminal proof (ADR-011): user message found, no unfinished correlated assistant, a terminal correlated assistant, native idle or absent. It deletes the guard only if it still belongs to this run. | `reconcile.go:375-395` (guarded delete at `391-394`) |
| Reconciliation runs only on `get_execution_status` / `get_execution_result`, after the monitor ended, serialized per run (`x.op`, single flight). | `reconcile.go:147-203` (gate at `:162`); `manager.go:468-471` |
| ADR-017 liveness and ADR-018 rejection are report-only and never consulted for the guard. | `reconcile.go:347-358` |
| Discovery reports a held guard's state and, since #93, skips the F-5 history read for guarded sessions. | `internal/mcp/discovery.go:146`; `internal/mcp/unfinished.go:62-76` |
| `SessionGuard` reads the guard without native I/O. | `manager.go:507-527` |
| ADR-010 lock files: one `flock` per allowed session in `BRIDGE_WRITER_LOCK_DIR`, held for the process lifetime; they fence other writers, not executions. | `internal/execution/ownership_linux.go:18-60` (`Flock` at `:55`); `cmd/bridge/main.go:51` |
| AUDIT-01 writes one slog line per `send_prompt` / `interrupt_execution` outcome. | `internal/mcp/audit.go:113-160`; `docs/CONTRACTS.md` "Mutation audit log" |

So a guard stuck on `unknown` stays stuck whenever correlated terminal proof
never appears. The three observed cases are:

- **Orphan (F-6):** an OpenCode restart left a correlated assistant with no
  `time.completed`. It never completes, and admission would refuse anyway (F-5)
  until the orphan is deleted natively.
- **Rejected permission (F-7):** the correlated assistant completed with
  `finish: tool-calls` and a rejected tool part. That is not terminal proof, and
  ADR-018 (d) deliberately does not promote it.
- **Lost Prompt:** the Prompt call's transport failed. The user message may or
  may not exist natively.

## 2. Options

### (a) Status quo: restart

Restart the Bridge after the ADR-010 fenced procedure. Every record is lost and
old keys must never be reused.

### (b) Operator-only fenced release

A **local** administrative command releases the guard of **one** session:

- **Transport:**
  - a Unix domain socket that only the writer process opens
    (`BRIDGE_ADMIN_SOCKET`, inside a 0700 directory owned by the service
    user, socket mode 0600);
  - the server checks `SO_PEERCRED` and accepts only the service UID; root
    operators use `sudo -u <service user>`;
  - a client subcommand of the same binary
    (`harness-bridge admin release-guard --session <opencode:ses_…> --execution <id> --ticket <ticket-id>`).
    `--ticket` is required and must match `[A-Za-z0-9._-]{1,64}`; the
    command accepts no free-text reason.
- **What it is not:** it is not an MCP tool, it has no TCP listener, and it is
  not reachable through the remote connector.
- **Compare-and-swap:** the request names the execution ID. The Bridge
  releases only if `busy[session]` is still that ID, so it can never release a
  newer execution's guard.
- **Bridge-side preconditions:** the operator's word is not enough. The Bridge
  itself checks:
  1. the run is `unknown`, its monitor has ended, and it is not terminal;
  2. a fresh identity and workspace check passes (`validateSession` before
     and `revalidateSession` after the native reads, as in the probe);
  3. native status is absent or `idle`;
  4. the **full** history has no unfinished assistant message (F-5 predicate);
  5. there is no pending permission for the session;
  6. the correlated user message is found. If it is absent, release needs an
     explicit `--allow-missing-user-message`, which is audited (the lost-Prompt
     case);
  7. a second read after a settle interval (proposed 5 s) shows the same
     message IDs and the same status;
  8. each of the two native reads holds the run's `x.op` token, so it never
     interleaves with a reconcile or an interrupt. `x.op` is **released during
     the settle sleep** and taken again for the second read. After re-taking
     it, the Bridge repeats the compare-and-swap and state checks (step 1 and
     "Compare-and-swap" above) before reading again. This matters because
     `refresh` and `Interrupt` wait for `x.op` only within the 5 s
     `reconcileTimeout` (`reconcile.go:139-143`, `:183-186`; `manager.go:119`,
     `:574`). Holding `x.op` through a 5 s sleep would make every
     `get_execution_status` / `get_execution_result` on that run time out
     during a release, and would hold one of the four reconcile slots. A
     reconcile that runs during the sleep may promote the execution (ADR-011);
     the re-check then refuses the release, because the run is terminal and
     its guard is already gone.
- **Effect:**
  - delete `busy[session]` (compare-and-swap under `run.mu` then `m.mu`, the
    existing lock order of `finalizeUnsubmitted`);
  - **the release itself never changes the execution's state, error,
    `unresolved` or `ended_at`, and never infers an outcome.** It leaves the
    record `unknown`. A later correlated native proof can still promote the
    old execution to terminal per ADR-011: the refresh gate excludes only
    terminal records (`reconcile.go:162`), and the terminal branch is
    unchanged (`reconcile.go:375-395`). This document states that choice here
    once; §3 ("Late native completion after release") and test 6 follow it.
  - **Why keeping ADR-011 promotion is safe:**
    - the guard map is keyed by native session and holds one execution ID
      (`busy[session] = id`, `manager.go:228-234`);
    - the promotion's delete runs only when `busy[x.native] == x.view.ID`
      (`reconcile.go:391-394`). After a release the entry is empty, or it
      holds a newer execution's ID, so the promotion deletes nothing;
    - proof is correlated by the exact native user message ID and the
      assistants' `parentID` (`reconcile.go:309`, `:313`), so a newer
      execution's messages can never promote the old one;
    - while a newer execution runs, its unfinished assistant (F-5) or native
      `busy` status blocks the old promotion's terminal branch.
  - **Rejected alternative: (ii) freeze released records as `unknown`.** It
    needs a reconcile change that skips released records, amends ADR-011, and
    throws away a real outcome the operator could use, while adding no safety
    beyond the guarded delete above. Test 6 would invert.
  - an additive `guard_released_at` field is recorded on the view;
  - idempotency mappings, fingerprints and the key fence are **kept**, so the
    old key still replays to the same `unknown` execution and never submits
    again;
  - lock files are untouched.

### (c) Automatic release on proof of quiescence

Reconciliation releases the guard, without promoting the state, when it
observes all of:

- the correlated user message found;
- every correlated assistant completed;
- no unfinished assistant anywhere in the history;
- no pending permission;
- native absent or `idle`;
- the same evidence on two probes at least a settle interval apart.

This covers F-7 directly and F-6 after the native deletion. It cannot cover
the lost-Prompt case: an absent user message is not proof that it will never
arrive. Failure modes:

- **Reconcile only runs on read.** A client that never calls `get_execution_*`
  never triggers it, so it would need a background prober, which brings new
  timing and load questions.
- **Background work.** OpenCode background jobs (task subagents in child
  sessions, `run-state.ts` `cancelBackgroundJobs`) can be active while the
  parent is idle. Whether they can append to the parent session later is
  **UNVERIFIED**, and quiescence of the parent alone may be wrong.
- **ADR-018 boundary.** ADR-018 (d) was accepted on the explicit premise that
  the rejection witness never affects the guard. (c) does not use the witness,
  but it releases in exactly the F-7 situation, so the maintainer should treat
  it as an ADR-018 amendment.
- **Silent error.** A wrong automatic release has no human in the loop and no
  ticket.

### (d) MCP tool for release

A ninth tool callable by the remote client. **Rejected:**

- **Authority:** the Chat principal could release its own guard and
  immediately submit again, removing the only Bridge-side double-submission
  barrier for the exact cases where the outcome is unknown.
- **Prompt injection:** native output and repository content are untrusted.
  An instruction in tool output could ask the supervisor to "release and
  retry".
- **Credential scope:** the connector token is a shared static credential
  (ADR-019). Its compromise would extend to guard bypass.
- **Policy:** the F-6 runbook and ADR-010 make destructive recovery a
  maintainer-only host action. An MCP tool would contradict both.

## 3. Analysis per option

| | (a) restart | (b) operator fenced release | (c) automatic on quiescence | (d) MCP tool |
| --- | --- | --- | --- | --- |
| Late native completion after release (ADR-011) | old record gone; a late completion is unobservable | record kept; reconcile can still promote the old execution, and its guarded delete (`reconcile.go:391-394`) cannot remove a newer guard | as (b) | as (b), but release can be requested before evidence settles |
| Double submission | old keys forgotten, so a reused key resubmits (documented ADR-010 limitation) | old key still replays (record and fingerprint kept); a new key is admitted only after the native admission preflight; residual: a lost Prompt arriving late, only with the audited override | as (b) minus the override (never releases without the user message) | a Chat-driven release-and-retry loop is possible |
| Races with a native TUI | unchanged (ADR-010 nonparticipating clients) | release does not change them; the next `send_prompt` re-reads native state (busy/unfinished/permission) in admission | same | same |
| Audit | systemd restart only; no per-session record | one new AUDIT-01 line per request (below) | one line per automatic release (new event) | Chat-attributed line; still Chat-controlled |
| Authorization | host root (systemd) | service UID via `SO_PEERCRED` on a private socket; root via `sudo -u` | none (automatic) | connector token holder |
| Multi-writer (ADR-010 locks) | locks released and re-acquired; another writer could win the race | locks untouched; only the lock-holding writer has the socket | locks untouched | locks untouched |
| Scope of loss | all sessions | one guard; nothing else | one guard | one guard |

Proposed audit line for (b), following the AUDIT-01 conventions (no prompt
text, no free text):

```
INFO guard release event=guard_release tool=admin outcome=released accepted=true session_id=opencode:ses_example execution_id=0f1e2d3c4b5a69788796a5b4c3d2e1f0 native_state=absent unfinished=false permission_pending=false correlated_user=found settle_ms=5000 ticket=OPS-1234 principal=admin:uid=998
INFO guard release event=guard_release tool=admin outcome=refused accepted=false error_code=SESSION_BUSY session_id=opencode:ses_example execution_id=0f1e2d3c4b5a69788796a5b4c3d2e1f0 native_state=busy principal=admin:uid=998
```

Proposed keys, in addition to the AUDIT-01 keys `event`, `tool`, `outcome`,
`accepted`, `error_code`, `session_id`, `execution_id` and `principal`:

| Key | Values | Notes |
| --- | --- | --- |
| event | `guard_release` | |
| tool | `admin` | never an MCP tool name |
| outcome | `released`, `refused` | `accepted` is `true` exactly for `released` |
| native_state | `absent`, `idle`, `busy`, `retry`, `unknown` | from the last native read; omitted if no read happened |
| unfinished, permission_pending | `true`, `false` | omitted if no read happened |
| correlated_user | `found`, `absent` | `absent` appears with `released` only with the override |
| settle_ms | integer | the settle interval used |
| ticket | `[A-Za-z0-9._-]{1,64}` | logged as given; a request without a valid ticket is refused (`INVALID_INPUT`) and its ticket value is not logged |
| principal | `admin:uid=<n>` | the peer UID from `SO_PEERCRED` |

No free-text reason is accepted or logged; the operator's explanation lives in
the ticket.

## 4. Recommendation (ACCEPTED 2026-11-08: option (b))

**Adopt (b).** Keep (c) as a possible later amendment once (b) has live
evidence and the background-job question is verified. Reject (d). Keep (a) as
the fallback and for token rotation (ADR-019).

### Minimal implementation outline

1. **`internal/execution`**
   - Add `(*Manager).ReleaseGuard(ctx, session, executionID string, opt ReleaseOptions) (ReleaseEvidence, error)`. It does:
     1. compare-and-swap lookup;
     2. state and monitor checks;
     3. take `x.op`; first native read through `x.runner` (Get + `validateSession`, Status, History, Permissions, `revalidateSession`); give `x.op`;
     4. the F-5 predicate, pending permission, correlated user message;
     5. sleep for the settle interval **without** holding `x.op`;
     6. take `x.op` again; repeat the compare-and-swap and state checks; second native read. Any check that fails refuses. `x.op` is **kept** through steps 7 and 8 (there is no sleep there), so no reconcile or interrupt can run between the second read and the delete;
     7. compare the two reads (message IDs, status);
     8. a final check under `run.mu` then `m.mu` that repeats **both** the compare-and-swap (`busy[session] == executionID`) and `state == unknown` with the monitor ended, then delete; give `x.op` only after this step;
     9. set `guard_released_at`.
   - It never changes `state`, `error`, `unresolved`, `ended_at`, `keys` or `fingerprints`.
   - Typed errors: `SESSION_BUSY` (native busy, unfinished or permission pending), `INVALID_INPUT` (not `unknown`, holder mismatch), `FORBIDDEN` (identity), `UPSTREAM_TIMEOUT` (bounded read timeout).
2. **`cmd/bridge`**
   - `BRIDGE_ADMIN_SOCKET`, opened only in writer mode, after `AcquireOwnership`. It checks directory ownership and 0700 mode, unlinks a stale socket only if it is owned by the service user, and checks `SO_PEERCRED` UID equals `geteuid()` per connection.
   - One request per connection, newline-delimited JSON, a size limit and a timeout.
   - A client subcommand `admin release-guard`.
3. **Audit:** `event=guard_release` lines as above, one per request on every return path.
4. **Docs:**
   - CONTRACTS.md: the admin socket, the additive execution field and the audit event;
   - ADR-010: a cross-reference;
   - the R5/R5b/F-6 runbooks: release instead of restart.

### Test plan

**Unit tests:**

1. Holder mismatch (a newer execution holds the guard) → refused, guard unchanged.
2. States `submitting`, `running`, `waiting_for_permission`, `completed`, `failed`, `interrupted` → refused.
3. Native `busy` / `retry`, an unfinished assistant anywhere, a pending permission, an identity mismatch, a read error, a bounded read timeout → refused, guard kept.
4. Correlated user message absent → refused; with the override → released and `correlated_user=absent` in the audit line.
5. Success:
   - `SessionGuard` is false;
   - the execution is still `unknown` with `guard_released_at`;
   - the same key replays the old execution with 0 new Prompt calls;
   - a new key is admitted with exactly 1 Prompt call.
6. Late completion after release:
   - reconcile promotes the old execution to terminal;
   - a newer execution's guard stays held.
7. History or status changes between the two settle reads → refused.
8. Concurrency: release racing with `refresh` / `Interrupt` on the same run serializes through `x.op` (`-race -count=50`).
9. Admin socket:
   - wrong peer UID → refused;
   - wrong directory mode or owner → startup fails;
   - not opened in read-only mode;
   - malformed or oversized request → refused, with one audit line.
10. MCP surface unchanged: still exactly the existing tools, none for release.
11. Audit line format; no prompt text, no free text; `ticket` charset and length enforced (too long or bad characters → refused).
12. Settle wait does not block pollers: a `get_execution_status` call on the run made during the settle sleep returns within the normal bound (no `reconcileTimeout` error).
13. Promotion during the settle sleep: a correlated terminal proof appears, a poll promotes the execution, and a newer execution then takes the guard. The release is refused and the newer guard stays held.

**Mutations that must be RED:**

- (i) release without compare-and-swap;
- (ii) skip the F-5 unfinished check;
- (iii) skip the pending-permission check;
- (iv) clear keys or fingerprints on release;
- (v) promote state on release (for example `failed`);
- (vi) skip the peer-UID check;
- (vii) skip the settle re-read;
- (viii) accept a missing user message without the override;
- (ix) make the late-completion delete unguarded;
- (x) hold `x.op` through the settle sleep (test 12 times out);
- (xi) skip the compare-and-swap and state re-check after re-taking `x.op` (test 13 releases or misreports);
- (xii) log the raw ticket without the charset check, or accept a free-text reason (test 11).

**Live acceptance** (could replace "restart the Bridge" in R5 Step 4 item 5, R5b Step 4.2 and F-6 step 4):

1. Reproduce a stuck `unknown` (for example R5b's rejection, or the R5 Step 4 OpenCode restart followed by the F-6 deletion).
2. As the operator, run `sudo -u <service user> harness-bridge admin release-guard --session <id> --execution <id> --ticket <ticket-id>`. Record the JSON evidence.
3. Check:
   - `get_session` is no longer guarded;
   - the execution is still `unknown`;
   - the journal has exactly one `event=guard_release outcome=released` line;
   - Bridge PID and `NRestarts` are unchanged.
4. Replay with the old key: same execution, no new `prompt_submission outcome=new` line.
5. Send with a fresh key: admitted, with exactly one `new` line.

## Limitations

- (b) still cannot prove what happened to the original prompt. It only makes
  the operator's decision explicit, local, audited and per session.
- The lost-Prompt override leaves a residual double-submission risk if the old
  request is delivered late. It is opt-in and audited.
- The settle interval is a heuristic, not a native lease. Native TUI races stay
  as in ADR-010.
- Token rotation (ADR-019) still needs a restart; (b) does not change that.
- Every line reference in §1 to §4 is to `main` `22264de`. The implementation
  (`internal/execution/release.go`, `internal/admin`, `cmd/bridge/admin.go`)
  is covered by unit, fake-runner and local-socket tests only; the live
  acceptance in §4 is still open.
