# MVP Development Backlog

| Order | Task | Acceptance | Risk |
|---|---|---|---|
| 0 | Verify OpenCode `/doc` from exact user's running version, message IDs, async state, SSE | Captured request/response traces | Undocumented linkage |
| 1 | Core Go OpenCode adapter | Existing server and session list/history; two-turn integration | Different server instance |
| 2 | Execution state and idempotency hardening | Busy, duplicate, disconnected, timeout and restart tests | Ambiguous submission |
| 3 | Official MCP Go SDK v1.8 migration | `go build/test/vet/race`, MCP inspector + transport tests | SDK compatibility |
| 4 | Workspace and per-session authorization | Cross-root and cross-principal denied | Wrong session |
| 5 | Secure remote HTTPS ingress and OAuth or verified fixed-token security | No unauthenticated access; actual Claude connector works | Public exposure |
| 6 | Real Claude Chat multi-turn experiment | Reads original history, sends prompt 1, observes completion, sends prompt 2 to same native ID | Claude tool loop limitations |
| 7 | Evidence and recovery | Tests/diffs verified, server restart behavior documented | False completion |

Not in MVP: Claude Code adapter, database, broker, frontend, external reasoning supervisor, automatic permission grant.

## Review follow-ups (2026-11-06, appended)

| Item | Source | Severity | Status / proposal |
|---|---|---|---|
| `internal/execution/reconcile.go:396-399` reports `running` when native status explicitly lists the session `idle` and there is no terminal proof. Unreachable on OpenCode 1.19.4, which deletes idle sessions from `/session/status` (F-4), so only a future upstream that keeps explicit idle entries would hit it. | R83-REVIEW (#86) finding 3 | P3 | Proposed: treat explicit `idle` without terminal proof like an absent entry (`OBSERVATION_UNAVAILABLE`, then `MONITOR_EXPIRED`), never `running`. Not implemented. |
| ADR-018 wording: the §1(b) rows "`reject` with message (`CorrectedError`)" and "config deny" say "always continues"; they should note that a `CorrectedError` reply still cascade-rejects every other pending request of the session (`RejectedError`), which can block the step, and that a cascade-rejected request also gets `permission.replied` `reject`. | R83-REVIEW (#86) finding 1 | nit | Fold into ADR-018 when the maintainer decides it. ADR-018 stays PROPOSED. |
| ADR-018 citation: the `running` assignment is at `reconcile.go:396-399` (the ADR cites `:393-399`). | R83-REVIEW (#86) finding 2 | nit | Fold into ADR-018 with the item above. |

### Review follow-ups: resolution notes (2026-11-06, appended)

- R83-REVIEW (#86) finding 1 (ADR-018 `CorrectedError` / config deny rows and the cascade-reject note): resolved by PR #99 (ADR-018 accepted, (d) on (a)).
- R83-REVIEW (#86) finding 2 (ADR-018 citation `reconcile.go:396-399`): resolved by PR #99.
- R83-REVIEW (#86) finding 3 (`running` on explicit `idle` without proof): still open; PR #99 does not change it.

### P3 cleanup: resolution notes (2026-11-08, appended)

- R83-REVIEW (#86) finding 3 (`running` on explicit `idle` without proof): resolved by PR #128. An explicit `idle` entry without terminal proof is now treated exactly like an absent one (`unknown`, `OBSERVATION_UNAVAILABLE`, then `MONITOR_EXPIRED`), never `running`.
- R93-REVIEW (#95) finding 1 (guard released between `needsHistory` and `view` untested): resolved by PR #128 (test-only hook and `TestGuardReleasedBetweenNeedsHistoryAndView`; the behavior was already fail-closed).
- R100-REVIEW (#102) nit 1 (startup error order): resolved by PR #128 (fixed: the token and ingress checks run before the native password; a missing or short token is reported first, as before ADR-019; order documented in CONTRACTS.md).
- R100-REVIEW (#102) nit 2 (no deterministic expiry-boundary test): resolved by PR #128 (one clock sample, injected-clock boundary test).
- R84-REREVIEW (#96) nits 1-2 (`on every connection` wording; ready-made Step 2.1 GET command in ORPHAN_MESSAGE_RECOVERY.md): resolved by PR #128.

### P3 cleanup 2: resolution notes and open items (2026-11-11, appended)

Resolved by PR #148 (`chore: P3 backlog cleanup 2`, task #146):

- ADR-020 / admin, from #125 and #126: the socket is `0600` only after bind and the `0700` parent directory is the real fence (CONTRACTS); cancellation of the settle sleep is an `UPSTREAM_TIMEOUT` trigger (CONTRACTS + `TestReleaseSettleSleepCancelledIsUpstreamTimeout`); the 4096-byte request limit counts the trailing newline (CONTRACTS + `TestRequestLimitCountsTrailingNewline`); the `x.op` hold during the second native read is pinned (`TestReleaseSecondReadHoldsOp`); concurrent admin connections are capped at 4 and extra ones are refused with the retryable `TOO_MANY_CONNECTIONS` (`TestConnectionCapRefusesExtra`, mutation: cap removed is RED); no client-side server peer check, same-UID attacker out of scope (CONTRACTS).
- Tests and seams, from #129, #130, #132 and #133: `guardWindowHook` global seam commented (why it is global); `TestReleaseLateCompletionKeepsNewerGuard` premise reworded (it proves the late promotion leaves the newer guard alone while the newer turn is quiesced; the busy-newer-turn case stays in `TestLateWrongCorrelationCannotReleaseGuard/busy`) and its wait hardened (30 s poll bound, explicit failure message); `TestProbeIdentityFSTimeoutNeverPromotes` V6 made independent of wall-clock timing (all `EvalSymlinks` of `/` block, so a stuck call expiring at `fsTimeout` cannot let a later probe complete the run).
- Docs, from #129 P3-4, #144 H4-F1 and H4-F2: Step 2.1 of `ORPHAN_MESSAGE_RECOVERY.md` documents the `"` and `\` escaping of the curl config line; CONTRACTS states that `interrupt_execution` returns the state at request time (poll `get_execution_status`) and that an aborted OpenCode bash part can read `completed` without an exit code (`interrupted` comes from `MessageAbortedError`, never from a part alone).

Open items recorded here, not fixed:

| Item | Source | Severity | Status / proposal |
|---|---|---|---|
| ADR-018 evidence table versus idle-as-absent: the evidence-table row for `TestF7PermissionRejectIdleStatus` (`docs/adr/ADR-018-permission-rejection-outcome.md:146`, and the `idle`-without-proof `running` report referenced at `:357`) says that native `idle` with no proof reports `running` until monitor expiry. Since PR #128 (2026-11-08) an explicit `idle` without terminal proof is treated exactly like an absent entry (`unknown`, `OBSERVATION_UNAVAILABLE`, never `running`). | #86 finding 3, PR #128 | P3 | Needs a maintainer amendment decision; ADR text is not changed by this task. ADR-018 stays as accepted. |
| P6 spike evidence file `docs/evidence/P6_SPIKE_EVIDENCE_2026-11-10.md`: three cosmetic P3s from review #144 (line numbers as cited there): a dangling `r1create` reference (the Step 1 text points to a block label that is not defined as such), malformed bold at :36 (the step 5 verdict-table row), and a stale "last writer wins" wording at :100 (the step 5 text, which the re-run block later says is not established). | #144 | P3 (cosmetic) | Recorded, not fixed: evidence files are immutable. |

### ADR-018 / idle-as-absent: resolution notes (2026-11-11, appended)

- ADR-018 evidence table versus idle-as-absent (the `TestF7PermissionRejectIdleStatus` row and the "Out of scope" bullet that still said `running` after PR #128; recorded as an open item in the "P3 cleanup 2" section above, from PR #148): resolved by the ADR-018 "Amendment 2026-11-11" (maintainer decision D10, text alignment only, no behavior change; the amendment was written together with ADR-024).

### ADR-023 slice 1 (dashboard skeleton): deferred items (2026-11-11, appended)

- `BRIDGE_DASHBOARD_SOCKET` and `BRIDGE_DASHBOARD_ADDR` are not yet documented in `docs/CONTRACTS.md` or the deployment docs; that is slice 5 (docs and live acceptance R-DASH, ADR-023 §7). Slice 1 only validates and serves the skeleton page.
- The dashboard has no data source yet: `Source` has one reader (`Sessions`) and the Bridge wires `NullSource`; the session views, diffs, audit ring and polling are slices 2 to 5.
- The unix-socket transport is Linux-only (like the admin socket); other platforms get a startup error that points at the loopback TCP address.
- The Host allowlist applies to the TCP transport only (ADR-023 T3, T9); on the unix socket the 0700 directory is the fence.

### ADR-023 slice 2 (sessions list/detail): deferred items (2026-11-13, appended)

- No production `Source` yet: the Bridge still wires `NullSource` in `cmd/bridge/dashboard.go`. The slice adds the read-only `Source` readers (`Sessions`, `SessionDetail`), the views, and `execution.Manager.DashboardSnapshot` (the T10 enumeration). The adapter that implements `Source` from the ACL sessions (the MCP discovery path, bounded) plus `DashboardSnapshot`, and wires it in `cmd/bridge`, is the next dashboard task; until then the dashboard shows "No data source is connected." and the ADR-023 slice 2 acceptance "the list matches MCP `list_sessions` for the ACL" is **not exercised**.
- D17 Q6: the ADR-022 lifecycle roots are not wired as a session source (ADR-022 S1 only parses the config; its tools do not exist yet). The code names the TODO and invents no source.
- D17 Q7: no archive overlay exists yet (ADR-022 S8, S6), so there is no archived row and no archive filter.
- `DashboardSnapshot` reports a guard whose holder is observed already terminal as held with state `unknown` (same conservative rule as `SessionGuard`), so a poll can show one stale "held (unknown)" row right after a release.
- Diffs (slice 3), the audit ring and polling (slice 4), and the live acceptance R-DASH (slice 5) are unchanged.

### ADR-023 slice 3 (diffs): deferred items (2026-11-13, appended)

- Still no production `Source`: the Bridge wires `NullSource`, so the diff views are exercised only with a fake `Source` over the fixtures. The adapter (ACL sessions, `Client.History` + `MessageInfo.DiffState` for the turns, `Client.SessionDiff`, `Client.VCSDiff` through a client bound to the session root, with its own deadline and no `Manager` slot) and its wiring in `cmd/bridge` are the next dashboard task. The slice 2 acceptance "the list matches MCP `list_sessions`" and the slice 3 acceptance "the diff matches `GET /session/:id/diff`" are **not exercised**.
- The native diff shapes are recorded from the upstream source at `148842e` and fixtured from it; **none is verified against a live OpenCode** (UNVERIFIED). The first live run must compare `/vcs/diff`, `/session/:id/diff` and `info.summary.diffs` with `testdata/*_148842e.*`.
- Operator redaction patterns exist as `Options.Redactor` only; reading them from configuration (variable or file) is not done.
- `GET /vcs/diff/raw` and `mode=branch` are recorded in ADR-023 section 1.2 but not offered by the views.
- The OpenCode client keeps its 8 MiB response bound; a `/vcs/diff` answer between 8 MiB and OpenCode's own 10 MB patch cap is shown as `unavailable`.
- `DashboardSnapshot` has no context and no cap by decision (review #223 P3-2; see ADR-023 "As built (slice 3)").

### ADR-022 slice S3 (list_catalog): deferred items (2026-11-14, appended)

- Startup wiring is not done: nothing sets `Server.Catalog` from `BRIDGE_LIFECYCLE_CONFIG`, so the production Bridge does not register `list_catalog` (ADR-022 3.1 default off). The wiring belongs with the first slice that makes the lifecycle configuration live (S4).
- The catalog lists ACL-authorized sessions only (ADR-012 is not amended; S9). If the maintainer wants every session under a lifecycle root listed, that is the S9 / ADR-012 amendment decision; see the ADR-022 "As built (slice S3)" note.
- The overlay has no persistence: `EmptyArchiveOverlay` is the only production implementation (S8, then S6). `include_archived` is therefore exercised only with a fake overlay.
- A natively archived session that `GET /session/:id` still returns is listed as an ordinary session (ADR-022 6.3 is UNVERIFIED about native archive visibility); nothing interprets `time.archived`.
- `root` is the lexical lifecycle root of the session directory (the directory is already ACL-authorized and symlink-checked); `by_root` counts only sessions that have one.
- Nothing ran against a live OpenCode: the native `time.updated` (epoch milliseconds) and `metadata` shapes are read from the existing session record and tested with a fake native server only (UNVERIFIED live).
- `core.bare` with an empty value is refused although git reads it as false (the scan errs towards refusing).

### ADR-023 slice 4 (audit ring, polling, limits): deferred items (2026-11-14, appended)

- Still no production `Source`: the Bridge wires `NullSource`, so the list, detail and polling JSON show "No data source is connected." and only the audit feed carries data; the R-DASH live acceptance stays NOT_EXERCISED. The adapter (ACL sessions, the manager's `DashboardSnapshot`, the native reads) and its wiring are the next dashboard task.
- The audit ring keeps `guard_release`, `prompt_submission` and `interrupt` only. The future lifecycle and permission events of ADR-022 and ADR-024 are shown when the allowlist is amended with them; their attributes must be added to the attribute allowlist in the same change.
- The page script was run only against a stub DOM under node, never in a real browser (UNVERIFIED): the first live run must watch the list and detail polling (304 on an unchanged model, a changed state appearing, hidden-tab slow-down) and the audit filter.
- The 4 / 8 limits are global to the dashboard listener, not per client: one local client can use all four data slots. On a unix socket or loopback with a single operator that is the intended shape; a per-client budget would need a client identity the transport does not give.
- The detail's polling model excludes the diff section; a changed diff needs a reload (the diff cache is 2 s).
- `Last-Modified` has one-second resolution and resets when the Bridge restarts (memory only), so a client holding an older validator across a restart gets a full 200 once.
- A page whose patches are hidden to fit the 2 MiB bound shows that per file; there is no "show anyway" path (the bound is the contract).

### ADR-023 slice 4 follow-up (audit tee swap, review #252 P2-1): notes (2026-11-14, appended)

- The tee's journal branch is a private `log.Logger` over the original writer (see the ADR-023 as-built note). It does not support the `log` flags `Lshortfile`/`Llongfile`, a second tee installed over the first (a second `openDashboard` in one process), or a custom default handler that itself writes through the `log` package; the Bridge needs none of them.
- One line can differ in the install window: a `log.Printf` logged between `slog.SetDefault` and the restore of the `log` writer is written as `INFO <message>` instead of `<message>`.

### ADR-022 slice S4 (create_session): deferred items (2026-11-15, appended)

- **No audit line, no production wiring.** AUDIT-01 `event=session_create` (ADR-022 4.3, slice S5) must land BEFORE `Server.Create` is built from `BRIDGE_LIFECYCLE_CONFIG` in `cmd/bridge`; until then the tool is registered only in tests. The create path writes nothing to the journal.
- The first prompt (4.1 step 6) and its derived key (`first 32 hex of sha256(key || ":first")`, with its 128-byte test) wait for O2 and slice S9; the helper is not shipped.
- The fake native's `GET /session?directory=&limit=` and `POST /session?directory=` shapes follow ADR-022 section 2; whether the real server filters the list by directory, honours `limit` and returns the new session's `directory` as sent is **UNVERIFIED live** (the count is therefore filtered and bounded on the Bridge side, and a longer-than-window list refuses).
- A created session is not in the ACL: the existing tools cannot act on it until the operator adds its ID (ADR-022 3.2 O1/O2/O3 is undecided, S9).
- The idempotency registry forgets the oldest finished record past 10,000 entries and everything at a restart (memory-only, ADR-022 3.3); a native 4xx/5xx is not recorded, so a caller retry after a 5xx that actually created a session would create a second one (the caps bound it).
- `create_session` is serialized (one at a time) so the caps hold; a slow native read delays the next create by at most its 5 s bound.
- Equivalent mutant noted in review: dropping the `..` component check in the tool still ends in `INVALID_INPUT`, because the S2 authorizer refuses the non-canonical joined path (two layers).

### ADR-023 slice 5a (production source, wiring, redaction config): deferred items (2026-11-15, appended)

- R-DASH is **NOT_EXERCISED**: the source is tested against a fake native only; the recorded native shapes (`summary.diffs`, `/session/:id/diff`, `/vcs/diff`) are still unverified against a live server.
- `BRIDGE_DASHBOARD_REDACT_EXTRA` (JSON array of patterns) is a proposal of this slice; the ADR did not name the source. The dashboard variables are still not documented in `docs/CONTRACTS.md` or the deployment docs (slice 5b/docs).
- Lifecycle-root rows (D17 Q6) and the archive overlay (D17 Q7) are not wired: `DashboardSource(nil)`; the overlay parameter exists so ADR-022 S6/S8 can plug in without a new contract.
- The derived discovery server has its own two discovery slots, so dashboard polling and MCP discovery do not queue behind each other; the combined native load is up to four concurrent discoveries.
- Claude Code rows are pending placeholders (state `unknown`); real state, history and diffs wait for the ADR-021 dashboard interface (the wiring, PR #264, is merged).
- Last activity shows native `time.updated` only; Bridge-observed activity (executions) is shown in the detail page.

### ADR-021 wiring (task #263, review fixes #268): deferred items (2026-11-14, appended)

- **`list_catalog` omits Claude Code sessions** (a deviation from ADR-022 section 6.2, which lists every harness's sessions, until a later slice adds the Claude rows with an instance-qualified `session_ref`). A `claudecode:` entry in `BRIDGE_ALLOWED_SESSIONS` no longer fails the catalog (it is skipped before the native client, like every discovery call); the Claude session is simply not a catalog row. `list_sessions` and `find_sessions` do list it when the adapter is enabled.
- Out of scope of the wiring slice, still open: the Claude history 8 MiB response bound (#266 P3-2), `BRIDGE_CLAUDE_HISTORY_MAX_AGE` retention (#266 P3-4, belongs to the enablement gate), ACL-before-allowlist ordering of the Claude session refusals (#267 P3-5), and the required-versus-optional wording of `BRIDGE_CLAUDE_HOME` and `BRIDGE_CLAUDE_MODEL` in the ADR (a maintainer item).

### ADR-022 slice S5 (session_create audit): deferred items (2026-11-15, appended)

- S5 audit landed for `create_session`; **production wiring from `BRIDGE_LIFECYCLE_CONFIG` is still a separate, planner-scheduled task** (the tool stays unregistered by default).
- Archive and close audit lines land with their tools (S6, after S8); they do not exist before.
- #262 P3-2 (a native 5xx on the POST: `UPSTREAM_FAILURE` versus `UNKNOWN_OUTCOME`) is an ADR 4.2 behaviour change: a maintainer item, not implemented.
- `root_id` is a configuration index; reordering the configuration changes the meaning of old journal lines. A stable root id in the lifecycle configuration would fix that (design change, not done).
- Live behaviour (what the real native server returns for a clamped `limit`, a created session's `directory`) is UNVERIFIED; all S5 evidence is against the fake native.

### D15 attempt writer (task #274): deferred items (2026-11-14, appended)

- The attempt writer is not wired: S5 adds `reply_permission`, the `mutating()` entry and the call site that makes no native write on any error from `audit.Writer.Write`.
- Latch recovery is "until process restart" (fail-closed minimum); other rules are maintainer proposals in the ADR-024 as-built note.
- The dashboard audit ring drops `permission_reply` (not in `auditEvents`), so attempts are in the journal but not in the ring until the dashboard allowlists are amended.
- A destination that reports a short write with a nil error is not detectable; journald delivery after an accepted write is not observable.
- Carry-overs #208/#210 P3s stay for the S3/S5 slice.

### ADR-024 S3 `reply_permission` reject (task #282): deferred items (2026-11-15, appended)

- Registration in the production server, the production `PermissionReplier` (the one `POST /permission/:requestID/reply` client method, routed by the session's directory/workspace like `PermissionView`), the production `PermissionSource` and the `audit.Default()` wiring land with S5; until then the tool cannot be called on a deployed Bridge.
- S5 owns the `phase=result` and `phase=refused` lines and the matching "result write failed after the native call, later calls refused" behaviour (the S3 row's test needs the result line).
- 401/403 from the native reply are treated as `UNKNOWN_OUTCOME` (conservative); decide whether a definite refusal exists for them.
- Latch recovery stays restart-only; the dashboard ring still drops `permission_reply`; `audit_unavailable` still uses `UPSTREAM_FAILURE` (maintainer proposals from #274).
- `list_catalog`/`get_session` expose no permission request detail beyond S1; a patterns exposure is policy mode only (D15 Q2 deferred).
- A hung attempt journal blocks the call (documented in ADR-024 4.2 step 8, not bounded).

### ADR-024 S5 `reply_permission` audit lines and registration (task #288): deferred items (2026-11-15, appended)

- **Production `PermissionSource` (own slice, needed before the tool can answer anything).** The observer needs a `PermissionObservationAuthority` (principal, native version check, policy revision, a read-only submitted-run view of the manager). Until then the registered tool fails closed and `pending_permission` is count-only. S1 and S3 were built against fakes for the same reason.
- S6 (runbook, ADR-018 section 5 status) is still open; the runbook should state `BRIDGE_PERMISSION_RELAY` and the per-call claude.ai approval recommendation (D15 Q7).
- Workspace-bound sessions use the prompt/abort routing (directory only); a workspace-routed reply is not built.
- Latch recovery stays restart-only, the dashboard ring still drops `permission_reply`, `audit_unavailable` still uses `UPSTREAM_FAILURE` (maintainer proposals from #274).
- A late journal write after a bounded attempt write timed out can leave an attempt line with no native write (over-reports, never hides).
- A result line that cannot be written is logged through slog, which uses the same failed journal: the loud log may itself be lost; the latch is the durable signal.

### ADR-023 slice 5b (lifecycle label, archived filter, docs): deferred items (2026-11-15, appended)

- R-DASH (the operator-present live acceptance with a real native and a browser) is **NOT_EXERCISED**; the native shapes behind the diffs remain unverified against a live server.
- Lifecycle roots are a label on ACL sessions (the `list_catalog` rule). Showing sessions that are under a lifecycle root but not in the ACL needs the O2 flag (ADR-012 amendment, D16 Q7): not done. `Server.Catalog` is not wired from `BRIDGE_LIFECYCLE_CONFIG` in `cmd/bridge`, so the production label is empty until that planner-scheduled task.
- The archive overlay is empty in production (ADR-022 S8, then S6); the filter and the fail-closed unknown state are tested with a fake overlay only.
- `BRIDGE_DASHBOARD_REDACT_EXTRA` is still a slice-5a proposal name; `docs/deploy/DASHBOARD.md` and `docs/CONTRACTS.md` mark it so.
- The `newMCPServer` test pins the nil-adapter case and the pointer identity of a zero-value `Harness`; a fully opened adapter is not constructed there.

### ADR-025 archive overlay persistence (ADR-022 slice S8, PROPOSED): deferred items (2026-11-15, appended)

- ADR-025 is a **design only, PROPOSED**: no Go code, nothing configured, `EmptyArchiveOverlay` stays the production overlay. It is not accepted (D12 does not name ADR-025); its thirteen open questions (section 9) stay with the maintainer, and slice S6 (`archive_session`, `close_session`) does not start before the maintainer accepts it.
- The proposed `BRIDGE_OVERLAY_DIR`, the file name, the 1000-entry and 512 KiB bounds and the `session_archived` refusal reason are proposals, not decisions.
- Whether an overlay-archived session refuses `send_prompt` (ADR-022 7.2 (B)) changes an existing tool and is open question 1; rollback detection (an external counter) is accepted as a residual in the proposal (T3) and is open question 7.

### ADR-021 slice 3 live driver (task #314): deferred items (2026-11-16, appended)

- **No live run was made.** The driver is tested against fake wrappers (shell scripts) and a simulated CLI; nothing about a real CLI, `sudo`, `setsid -w` or the managed file is verified. The live run is a separate task and the items of `ACCEPTANCE_S3.md` stay NOT_EXERCISED.
- The MCP finding (31 account-synced servers) is a hypothesis from #305: the entries were not inspected. The proposed tightening (`disableClaudeAiConnectors` in the inline and managed settings, `ENABLE_CLAUDEAI_MCP_SERVERS=false` in the child env) is documented in ADR-021 §8.3 but **not applied**; the pinned argv and `CheckArgv` are unchanged. Whether a managed `env` block honors the variable is not documented.
- The driver's decoy files must be chowned to the run user, so it needs a privilege the planner's task must provide; the driver cannot `cd` into a 0700 root of another user (the wrapper's own working directory decides). Item 6 does not run the pre-flight. The history comparison, the file-canary scan and the sink observation are not made by the production command (no `Observer`): the evidence prints them as `unknown`, the verdict is `incomplete` and item 1 is `INCOMPLETE` (exit 3), never PASS (R317-FIX-1 / R317-FIX-2).
- `ProcFS.Alive` scans `/proc/*/cmdline` for the run's session UUID; a process that hides or rewrites its command line would be reported gone.
- **R317-FIX-1 (#326, review #323).** `-service-uid` is required (never 0, never equal to `-run-uid`). The child must run as the isolated test user with that user's HOME: the driver cannot set HOME through `sudo`, so the operator provisions the wrapper/sudo (operator step, a proposal; no host file is edited) and the evidence prints `precondition child_home=operator_precondition_not_verified` (there is no attestation input, so the line does not say "attested"; a later task may add a verified variant if an Observer can check the child HOME). The history comparison, the file-canary scan and the sink observation have no production implementation: item 1 reports `INCOMPLETE` (exit 3), never PASS, and the evidence prints `unknown` for them, until an `Observer` supplies them (fakes only in tests). The driver removes only files and directories it created.
- **Production `Observer` follow-up, child identity (#340 P3-1, recorded only).** Instead of the operator precondition `operator_precondition_not_verified`, a later task should record the child's real identity: as root the driver already scans `/proc/*/cmdline` for the session UUID (`ProcFS.Alive`), so it can read that process's `/proc/<pid>/status` (`Uid:`) and `/proc/<pid>/environ` (`HOME=`) and print `child_uid=<n> child_home_matches=<bool>`; item 1 would then be INCOMPLETE only when this is unreadable. It reads the driver's own child, not a credential file; nothing is implemented here.
- **R317-FIX-3 (#345, security review #340 P2-1).** `OSDecoyFS` no longer operates by path inside the run user's HOME: it walks from `/` with `O_DIRECTORY|O_NOFOLLOW`, creates with `openat(O_CREAT|O_EXCL|O_NOFOLLOW)`/`mkdirat`, chowns the open descriptor, keeps a descriptor on every object it created (so its inode cannot be recycled), and removes with `unlinkat` after a device/inode re-check; a symlink in the `-run-home` path or a swapped entry is refused. It is unix-only (other platforms refuse every operation). Residual: a directory the driver created is verified as a directory owned by the driver's own uid before it is chowned; a hostile swap to a root-owned directory that the run user could rename into place is not detected (it requires a root-owned directory inside a tree the run user writes). Residual (b) (#346 P3-2): ancestors above the run home are checked for symlinks only, not for ownership or mode (the path is operator-chosen); swapping an ancestor for another real directory only moves the work into a tree the attacker already controls, and `Remove` refuses any entry whose device/inode differs from the record. The header no longer suggests a file capability or a build in `/tmp`.
- **R317-CLEANUP (#349, reviews #338 #346).** The owner check in `mkdirOwned` now has a test (a seam between `mkdirat` and the no-follow reopen swaps the directory; the owner read is stubbed non-root; mutation M4 is RED); the evidence file (`-out`) is created through the no-follow walk with `O_EXCL|O_NOFOLLOW` (`acceptance.CreateEvidenceFile`). #338 P3-N1 (a directory whose chown failed is removed with `unlinkat` only while it is still the recorded object; if that removal itself fails it is left behind unrecorded) stays a cosmetic residual: it is unreachable in practice because the creator owns the directory and the item has already failed. #338 P3-N2 (`AllObserved` as a public value) is closed by #345 (`allObserved` is unexported and pinned by a test). Residual (a) hardening (require the reopened directory to be empty and recent) is not done: it does not change who can move a directory into place.

### ADR-024 S6 production `PermissionSource` (task #304): deferred items (2026-11-16, appended)

- **Native incarnation evidence is missing, so a deployed Bridge still cannot verify or answer a request.** The source is wired and tested against a fake native with an injected epoch, but production supplies none (`execution.NativeIncarnation`, nil): every request is `unattributed`, `pending_permission` is count-only and `reply_permission` refuses `FORBIDDEN`/`not_verified` (no native write, no attempt line). Maintainer decision needed: what counts as independent native incarnation evidence for OpenCode 1.19.4 (a native boot or instance identifier read from the server, if one exists; or an operator-supplied value that the operator rotates on a native restart, which is weaker). Not fabricated from event IDs (P2_PERM_CORE_02_OBSERVATION). Not run live: no native identifier was looked for on a real server.
- The R5b-style live acceptance of `reply_permission` (runbook, `docs/deploy/PERMISSION_RELAY_RUNBOOK.md`) is **NOT_EXERCISED**; its expected result today is the `not_verified` refusal.
- The authority's refs (`root-`/`workspace-`) are opaque hashes of the run's canonical directory; a finer project-root reference (ADR-022 lifecycle roots) is not wired.
- A fresh observer per call costs the observer's reads (session, permission list, and per request the assistant and user messages) on each reply and on each reconcile pass that sees a pending request; not measured against a live server.
- Workspace-bound sessions still route the reply by directory only (S5 note); the policy mode (S2/S4) and `allow_once` stay not started (D15 Q1, D12 (b)).

