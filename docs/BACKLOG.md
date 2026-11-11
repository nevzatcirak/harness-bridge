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

### ADR-023 slice 1 (dashboard skeleton): deferred items (2026-11-11, appended)

- `BRIDGE_DASHBOARD_SOCKET` and `BRIDGE_DASHBOARD_ADDR` are not yet documented in `docs/CONTRACTS.md` or the deployment docs; that is slice 5 (docs and live acceptance R-DASH, ADR-023 §7). Slice 1 only validates and serves the skeleton page.
- The dashboard has no data source yet: `Source` has one reader (`Sessions`) and the Bridge wires `NullSource`; the session views, diffs, audit ring and polling are slices 2 to 5.
- The unix-socket transport is Linux-only (like the admin socket); other platforms get a startup error that points at the loopback TCP address.
- The Host allowlist applies to the TCP transport only (ADR-023 T3, T9); on the unix socket the 0700 directory is the fence.
