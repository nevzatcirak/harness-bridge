# Bounding synchronous filesystem calls in `internal/execution` (PROPOSAL)

Status: **PROPOSAL** (design only; no code). Task EXEC-FS-BOUND-DESIGN, issue #51.
Date 2026-11-05, Worker C. Base `main` `7adbff6` (includes #41 and #50).

ADR-010 and ADR-011 (accepted) bind this design; ADR-017 (PROPOSED) is checked
for consistency. Background: Worker A (#48) found synchronous `EvalSymlinks`/`Stat`
calls on the `send_prompt` and reconciliation paths. A directory on a hung mount
(NFS/FUSE) blocks such a call indefinitely, and no Go context can interrupt it.

## 1. Inventory

The list was produced with
`grep -n 'os\.\(Stat\|Lstat\|Open\|ReadFile\|ReadDir\)\|filepath\.EvalSymlinks\|syscall\.\(Open\|Openat\|Fstat\|Stat\)'`
over `internal/execution/*.go` (excluding tests), plus every caller of
`validateSession`. `os.SameFile` compares two `FileInfo` values already in
memory, so it does no I/O.

### 1.1 Primitive calls

| # | Location | Call | Purpose |
|---|---|---|---|
| P1 | `manager.go:270` | `filepath.EvalSymlinks(directory)` | Admission: the native session directory must be canonical (no symlink, clean). |
| P2 | `manager.go:276` | `os.Stat(canonical)` | Admission: capture the workspace inode that every later check compares against. |
| P3 | `reconcile.go:78` | `filepath.EvalSymlinks(meta.Directory)` | `validateSession`: the directory still resolves to the accepted canonical path. |
| P4 | `reconcile.go:82` | `os.Stat(canonical)` | `validateSession`: same inode as at admission (`os.SameFile`). |
| P5 | `reconcile.go:248` | `filepath.EvalSymlinks(msg.Info.Path.Cwd)` | Probe: each correlated assistant message ran in the accepted workspace. |
| P6 | `ownership_linux.go:22,28,43,49` | `syscall.Open`, `Fstat`, `Openat`, `Fstat` | Startup only (`cmd/bridge/main.go:54`): writer lock directory and per-session lock files. `Flock` is `LOCK_NB` (`:55`). |

### 1.2 Call sites of `validateSession` (P3 + P4)

| # | Location | Path |
|---|---|---|
| V1 | `manager.go:191` | `SendWithRunnerOutcome` replay (same key and payload) |
| V2 | `manager.go:385` | `work` pre-submission recheck |
| V3 | `manager.go:583` | `Interrupt`, before `Abort` |
| V4 | `reconcile.go:96` | `ValidateSession`, the MCP binding for status, result and interrupt (`internal/mcp/server.go`) |
| V5 | `reconcile.go:118` | `refresh`, identity check of the cached result |
| V6 | `reconcile.go:197` | `probe`, before the native reads |
| V7 | `reconcile.go:217` | `probe`, after the native reads |

## 2. What a block does today

"Held" lists the state, guards and locks held while the call blocks. None of
these calls honours a context, so every wait or deadline is ignored until the
syscall returns.

| Site | Phase | Held while blocked | Effect today |
|---|---|---|---|
| P1/P2 | Admission, before any native submission (`submissionAttempted` false, key not accepted) | Session guard reservation `m.busy[session]` (`manager.go:217`), `m.pending[k]`, one of 4 `admissionSlots`, `m.wg` count | The `send_prompt` call hangs. Other keys for the session get `SESSION_BUSY`; same-key waiters block until their own ctx. After four such hangs every admission waits out its 5 s ctx. `Close()` hangs in `m.wg.Wait()` (`manager.go:113`). |
| V1 | Replay of an accepted execution | nothing (after `m.mu.Unlock()`) | The `send_prompt` replay request hangs. The execution is unaffected. |
| V2 | Accepted, before `Prompt` (`submissionAttempted` false) | Session guard (published run), `m.wg` (`work` goroutine) | The run stays `submitting` past `m.timeout`, because the work ctx is not consulted during the syscall. The guard is held. `Close()` hangs. |
| V3 | After submission (interrupt) | `x.op`, one of 4 `reconcileSlots`, `m.wg` (`beginRead`) | The interrupt request hangs. Monitor, probe and refresh for this run wait on `x.op`. A reconcile slot is consumed, so after four such hangs reconciliation starves for all runs. `Close()` hangs. |
| V4 | Read binding (status, result, interrupt) | nothing | The MCP request hangs. The comment "No native I/O occurs here" (`reconcile.go:89-90`) is true for native I/O but misleading about filesystem I/O. |
| V5 | Read (`refresh`) | nothing | The status request hangs. |
| V6/V7 | Observation after submission (monitor `observe` or `refresh`) | `x.op` (and a reconcile slot on the refresh path), `m.wg` | The monitor stalls past its 5 min lifetime. Interrupt and refresh for the run wait on `x.op`. `Close()` hangs. |
| **P5** | Observation after submission | **`x.mu`** (plus `x.op`, slot, `m.wg`) | **Worst case.** Every `x.mu` user blocks: `Snapshot` (status, result, interrupt authorization), `SessionGuard` (`manager.go:478-496`, called by discovery `view()` at `internal/mcp/discovery.go:134`) and `Close()`. Discovery for that session then hangs past its own 10 s deadline, because a mutex is not ctx-aware. With 2 discovery slots, two such calls stop all discovery. |
| P6 | Startup | none (no listener yet) | The Bridge never starts serving. This is fail-closed by nature: no writer, no mutation. |

## 3. Proposed bounded behaviour

### Principles

1. **Before any native submission**, a filesystem timeout is a typed, retryable
   rejection. Nothing is sent, the key is not accepted, and the guard
   reservation is released through the existing `release()` path.
2. **After submission**, a timeout is never evidence. The execution stays (or
   becomes) `unknown`, is never promoted to a terminal state, and the guard is
   never released on a timeout (ADR-011). A timeout is not `IDENTITY_MISMATCH`:
   only a completed check that disagrees is a mismatch.
3. **Read paths** that only authorize or expose a snapshot return a typed,
   retryable error and expose nothing that was not re-validated.
4. No positive filesystem result is cached beyond the call that obtained it
   (TOCTOU: the identity check exists to detect replacement).
5. An abandoned syscall goroutine is never counted in `m.wg`, so `Close()`
   cannot hang on it. The number of such goroutines is bounded.

### Error code

Use **`UPSTREAM_TIMEOUT`**, which is already in the CONTRACTS enum
(`docs/CONTRACTS.md:34`) and unused today, with **`retryable: true`** and a
message naming the local workspace check. Justification:

- `FORBIDDEN` (today's code for a failed `EvalSymlinks`/`Stat`) is not retryable
  and asserts a fact the Bridge does not know.
- `UNKNOWN_OUTCOME` would wrongly suggest a submission may have happened.
- `UPSTREAM_FAILURE` is not retryable today.

This is the first use of `retryable: true`. The implementation PR must document
it in CONTRACTS as a contract addition. It is safe only on the paths below where
nothing was or will be submitted.

### Per site

| Site | Bounded outcome | State / guard after timeout |
|---|---|---|
| P1/P2 admission | `release()`, return `UPSTREAM_TIMEOUT` (retryable, "workspace filesystem check timed out; nothing submitted"). The existing deferred cleanup closes `pending[k]` and drops the fingerprint because the key was never accepted (`manager.go:227-235`). | No run created; guard released; the same key may be retried and is a new admission. |
| V1 replay | Return `UPSTREAM_TIMEOUT` (retryable). Do not return the snapshot, because its identity is unverified. | Execution untouched. |
| V2 work pre-submit | Treat like any validation failure: `finalizeUnsubmitted(x)` (`manager.go:340-362`). | `failed` with `PRE_SUBMISSION_UNAVAILABLE`. The guard is released because `Prompt` provably was not invoked (`submissionAttempted` is set only after this check, `:404`); this is the ADR-011 release rule. The key stays a tombstone, and a replay returns the failed execution. No resubmission. |
| V3 interrupt | Do not call `Abort`. Return `UPSTREAM_TIMEOUT` (retryable, "interruption not submitted"). Release `x.op` and the slot via the existing defers. | State unchanged (still `running`/`unknown`); guard kept. |
| V4 `ValidateSession`, V5 `refresh` | Return `UPSTREAM_TIMEOUT` (retryable). The MCP layer then returns no snapshot (the error happens before `Get`/`Result`). | Execution untouched. |
| V6/V7 probe | `failed("OBSERVATION_UNAVAILABLE", "workspace identity check timed out", nil)`. As for other unavailable observations, the status call still succeeds and shows `unknown`. | `unknown`, `observationValid=false`, no terminal commit; guard kept; `x.op` released. |
| P5 cwd check | **Move it out of `x.mu`.** Collect the distinct `Path.Cwd` values of correlated assistant messages, resolve them through the bounded wrapper **before** taking `x.mu`, then apply the results under the lock. Timeout → as V6 (`OBSERVATION_UNAVAILABLE`, not `IDENTITY_MISMATCH`). A completed resolution that differs from `x.canonicalDirectory` stays `IDENTITY_MISMATCH`. | `unknown`; guard kept; `x.mu` is never held across a syscall. |
| P6 startup | Out of scope. Optionally a startup deadline: exit non-zero if the lock directory checks do not finish, so a supervisor reports it. | No serving, no writer. |

The deadline for each call is `min(caller ctx, fsTimeout)`, with `fsTimeout = 2s`
(as `inspectTimeout`, `internal/repository/repository.go:137`). Admission
keeps its outer 5 s `readContext` (`manager.go:225`). The `work` goroutine's
5 min ctx also caps V2.

### Consistency with the ADRs

- **ADR-010.** Keys and fingerprints are unchanged.
  - A1 rejects before the key is accepted, so it cannot create a replayable record.
  - V2 keeps the tombstone.
  - No restart or reuse semantics change.
- **ADR-011.** Inconclusive evidence keeps `unknown` and the guard (V3, V6, V7, P5).
  - The only releases are the two existing provable non-submission paths: A1 `release()` and V2 `finalizeUnsubmitted`.
  - No inferred success, no resubmission, no `Abort` without a completed identity check.
- **ADR-017 (PROPOSED, report-only stall liveness).** A filesystem timeout during observation is one more `OBSERVATION_UNAVAILABLE` cause.
  - It must not count as stall evidence or as a native signal.
  - It must not trigger any guard release ADR-017 might later allow.
  - The `Unresolved.Reason` value keeps it distinguishable.

## 4. Mechanism

Reuse **`internal/bounded.Group`** if #48 lands it (planned there: per-key single
flight, cap on calls still running, detached work context, wait bounded by the
caller's ctx or 2 s). Otherwise copy the discipline of the merged
`repository.Inspect` wrapper (`internal/repository/repository.go:136-218`).

For `internal/execution`:

- **A dedicated `Group` instance**, so saturation from discovery or `Policy.Allows`
  cannot starve admission and observation, and the reverse. Cap **8**.
  - At the cap, the call fails closed at once without starting a goroutine,
    using the per-site outcome above.
  - This also bounds the goroutines a hung mount can pin.
- **Key** = (operation, path), e.g. `("evalsymlinks", "/w")` and `("stat", "/w")`.
  Joiners share one in-flight result. The result is dropped once the last
  caller has left; it is not cached.
- **Two thin wrappers**, `evalSymlinks(ctx, path)` and `stat(ctx, path)`, used by
  `SendWithRunnerOutcome`, `validateSession` (which gains a `ctx` parameter) and
  the pre-resolution step for P5.
  - A timeout returns a sentinel (`errFSTimeout`). Each call site maps it per
    the table above.
  - Any other error keeps today's meaning: `FORBIDDEN` or `IDENTITY_MISMATCH`.
- **Hook for tests:** `SetFSHookForTesting(func(op, path string))`, as in
  `repository.SetFSHookForTesting`. It is nil in production.

## 5. Test plan

The tests use the existing fake runner (`internal/execution/manager_test.go`)
plus the FS hook to block a chosen operation and path until released. Each
blocking test runs the call in a goroutine and asserts completion with a bounded
`select`, so a regression shows as RED, not as a hung test.

| Test | Asserts |
|---|---|
| Admission blocked (P1, P2) | Returns within about 2 s with `UPSTREAM_TIMEOUT`, `retryable:true`. `Prompt` count 0, `SessionGuard` false, a same-key retry after unblock is a **new** admission. |
| Replay blocked (V1) | Error, no snapshot. After unblock, the replay returns the original IDs. |
| Pre-submit blocked (V2) | `failed` with `PRE_SUBMISSION_UNAVAILABLE`. `Prompt` count 0, guard released, the same-key replay returns the failed run, and there is no second `Prompt`. |
| Interrupt blocked (V3) | `UPSTREAM_TIMEOUT`, `Abort` count 0. State and guard unchanged. `x.op` is free afterwards (a probe can run). |
| `ValidateSession` and `refresh` blocked (V4, V5) | Typed error within the deadline. |
| Probe blocked (V6, V7) with native terminal proof available | Stays `unknown` with `OBSERVATION_UNAVAILABLE`, **no promotion**, guard held. After unblock the next probe promotes normally. |
| cwd blocked (P5) | As V6. **While blocked**, `Snapshot`, `SessionGuard` and a discovery `view()` for the session return within 100 ms (`x.mu` not held). |
| Shutdown | `Close()` returns within its bound while a call is blocked. |
| Saturation | With 8 blocked calls the 9th fails closed immediately. Goroutine growth is bounded (poll-and-settle, as in #41/#44). |
| Recovery | After unblock, all paths succeed again. Late results from abandoned calls are dropped. |
| Race | `-race -count=20 ./internal/execution ./internal/mcp`. |

**RED on base:** the same tests on `main` time out in their bounded `select`
(hang) or show the `x.mu` stall.

**Mutations (each must turn RED):**
1. Remove the ctx/deadline `select` from the wrapper.
2. Map the timeout to `IDENTITY_MISMATCH` instead of `OBSERVATION_UNAVAILABLE`.
3. Treat a timeout as a passed check.
4. Release the guard on a probe timeout.
5. Resolve the cwd under `x.mu` again.
6. Omit `release()` on an admission timeout.
7. Set `retryable:false` on the admission error.
8. Call `Abort` after a V3 timeout.

## 6. Limits and open points

- A syscall stuck in the kernel keeps its goroutine and possibly a kernel
  thread until the mount recovers. The design bounds how many such goroutines
  exist; it cannot reclaim them.
- The check stays a best-effort detection of observed replacement, not an
  atomic lease (unchanged from today, `manager.go:381-382`).
- **UNVERIFIED:** the final `bounded.Group` API from #48 (ctx semantics and the
  cap-reached result). The design assumes the shape stated in #48.
- `UPSTREAM_TIMEOUT` with `retryable:true` is a contract addition and needs
  CONTRACTS.md, and client guidance: retrying the same key after a timeout is
  safe only on the paths listed in §3.
- P6 stays out of scope. Startup blocking cannot cause a mutation.

## 7. Recommendation

1. Implement after #48 merges, as one `internal/execution` PR with the test
   plan above and an independent review. It changes execution semantics only on
   the timeout paths listed; all other behaviour is pinned by the existing tests.
2. **Priority: P5 first** (holding `x.mu` across a syscall can stop discovery and
   status for the session). Then admission P1/P2 (guard reservation and shutdown
   hang). Then V2–V7.
3. Until then, operators should keep ACL roots off network or FUSE mounts. The
   policy requires canonical absolute paths but does not exclude such mounts,
   so this is an operating rule, not an enforced one.
