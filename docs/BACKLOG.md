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
