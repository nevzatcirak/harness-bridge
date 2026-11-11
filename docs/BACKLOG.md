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

### ADR-018 / idle-as-absent: resolution notes (2026-11-11, appended)

- ADR-018 evidence table versus idle-as-absent (the `TestF7PermissionRejectIdleStatus` row and the "Out of scope" bullet that still said `running` after PR #128; recorded as an open item in the "P3 cleanup 2" section of PR #148): resolved by the ADR-018 "Amendment 2026-11-11" (maintainer decision D10, text alignment only, no behavior change; the amendment was written together with ADR-024).
