# R3a evidence — real Claude Chat → Harness Bridge → existing OpenCode session

Date: 2026-11-03 (UTC). Executed from the Planning Desk Claude Chat conversation (normal claude.ai chat,
custom connector "Harness Bridge", Max plan, static Request header). Status: CORE PASS, two operator
records pending (see "Open items").

## Runtime
- Bridge: H3 `4cb09fb6243fcc787ae2fe08c17c367fbd06fdf7`, binary sha256 `05000a48…fd73` (verified for PIDs
  572369 and 2666494; PID 2682513 hash pending). Writer mode via drop-in `90-h3-writer.conf`.
  Credential rotated by operator 13:56Z; expiry ≤ 24 h.
- Writer lifetimes during the day: PID 572369 (until 14:00:29Z, expired credential); PID 2652081
  (14:00:30–14:35:03Z, after rotation; carried the 14:03Z provider-failure submission and the 14:30Z
  interrupt; exe hash not independently verifiable after exit); PID 2666494 (14:35:40–15:13Z; carried the
  14:55Z model-not-found submission and the busy probe); PID 2682513 (from 15:13:02Z, successful run).
- Successful run writer lifetime: PID 2682513 (started ~15:13Z, after the guard-clearing restart).
- OpenCode 1.19.4 test instance `127.0.0.1:4097`, PID 2681945 at run time.
- Model: agent `build` → `openrouter/z-ai/glm-5.3-flash` (opencode.jsonc sha256 `9f3b7417…5457`;
  drop-in `zz-openrouter.conf` sha256 `59ca159f…0e2f`). Assistant parts carry `metadata.openrouter`
  and non-zero cost, evidencing the OpenRouter provider; the H3 result DTO does not expose modelID.
- Session `opencode:ses_e9329c0b4ffebOlm7GRyKxbMQ5`, cwd `/opt/harness-bridge-test/workspace`.

## Successful run (call log)
| # | Tool | Key args | Result | UTC |
|---|---|---|---|---|
| 0 | get_session | ses_unauthorized_probe | FORBIDDEN, retryable=false | — |
| 1 | get_session | target | identity + cwd match | — |
| 2 | send_prompt | key r3a3-t1-5632632088ee701d, marker HB-R3A3-14845fd87db341e8b6fdcb561d766a1e | exec cba685ab9a78e5406f84b550d19e056f, msg msg_1a1f944850000369b55ffce80e, submitting | 15:13:50.213Z |
| 3–4 | get_execution_status | exec cba685ab… | running | 15:13:52.246Z, 15:13:53.749Z |
| 5 | get_execution_status | exec cba685ab… | completed, ended_at 15:13:54.058Z | 15:13:54.246Z |
| 6 | get_execution_result | exec cba685ab… | assistant msg_1a1f94768001suwBl7pVIgIRKb, parent = turn-1 user, finish stop, text = marker + cwd | — |
| 7 | send_prompt (REPLAY) | same key, identical payload | same exec cba685ab…, same msg msg_1a1f9448…, completed | — |
| 8 | send_prompt | key r3a3-t2-c6fc908aea134bc9, prompt without marker | exec 262360f67a9f76870b68238d854e34a5, msg msg_1a1f98fce00045e0bfaae87b04 | 15:14:09.487Z |
| 9 | get_execution_status | exec 262360f6… | completed, ended_at 15:14:10.817Z | 15:14:11.029Z |
| 10 | get_execution_result | exec 262360f6… | assistant msg_1a1f98fe2001ytazuFDcLzVSNV, parent = turn-2 user, finish stop, text = HB-R3A3-14845fd87db341e8b6fdcb561d766a1e /opt/harness-bridge-test/workspace | — |
| 11 | read_session_history | target | 23 messages (see counts) | — |

## Counts (computed from message ID lists, not estimated)
- Before run: 19 messages = P4-EVID-01 baseline 16 + msg_1a1b8c18f (user, provider failure) +
  msg_1a1b8c1cb (assistant, empty) + msg_1a1e82188 (user, model-not-found failure).
- After run: 23 = 19 + msg_1a1f9448 (U1) + msg_1a1f94768 (A1) + msg_1a1f98fce (U2) + msg_1a1f98fe2 (A2).
- First 19 IDs unchanged and in the same order. Exactly one user message carries the turn-1 text:
  the replay added zero native user messages.

## Verdict per criterion
| Criterion | Result |
|---|---|
| Unauthorized session → typed FORBIDDEN | PASS (three writer lifetimes) |
| Identity, cwd unchanged | PASS |
| Turn 1 completed, correlated parent, final stop | PASS |
| Same-key replay → identical execution/message IDs, zero new native user messages | PASS |
| Turn 2 prompt without marker; marker recalled | PASS |
| Exactly +2 native user / +2 assistant messages for the run | PASS |
| Busy session: different key while an uncertain execution held the guard → SESSION_BUSY, zero native prompt | PASS (PID 2666494, key r3a2-busy-9c1e4b7a52d0; history stayed at 19) |
| Uncertain execution handling (provider failures) | PASS: no success inferred, no replay, guard held (exec c76148d6…, d846680d…) |
| Interrupt path | Abort sent and confirmed; Bridge did not promote without terminal evidence (exec c76148d6…) |
| ADR-011 live late completion (unknown → terminal) | NOT_EXERCISED (no late native completion occurred) |
| Auth negatives against the rotated writer | PASS (R3A-STEP2-RERUN 15:28:25–26Z, PID 2682513 unchanged): no header → 401 Bearer, wrong token → 401 Bearer, both logged `remote authentication accepted=false` after `boundary_allowed=true` (auth layer, not expiry); wrong Origin → 403 (`boundary_allowed=false`); `/not-mcp` → 404 at Traefik, no Bridge log; 0 × 2xx |
| Same writer PID throughout successful run | PASS: PID 2682513, ActiveEnter 15:13:02Z, NRestarts=0, exe sha256 05000a48…fd73 (operator, after the run) |
| Model actually used | PASS: opencode log 15:13:51Z and 15:14:09Z `providerID=openrouter modelID=z-ai/glm-5.3-flash` (run 736036af) |

## Findings
1. OpenCode 1.19.4 does not persist provider stream errors on the assistant message; abort adds only
   `time.completed`. Bridge correctly stays `unknown`, but recovery required a Bridge restart
   (liveness gap; candidate R5 item "native stalled").
2. Session-level `state` is always `unknown` in H3 by contract; the R3a instruction that used it as a
   stop condition was wrong (fixed in the runbook).
3. Model-estimated counts in the first acceptance conversation were wrong (12/14 vs actual 16/18).
   Acceptance counts must be computed from ID lists.
4. systemd drop-ins apply in lexical order; `auth.conf` overrode the first OpenRouter drop-in's
   ExecStart. Fixed by `zz-openrouter.conf`.
5. Historical (2026-10-25) native history already contains a marker-recall pair (`HB-H4-7f3a…`);
   note for P4-EVID-01.

6. Bridge journal logs `prompt submission accepted=true` three times (15:13:50 turn 1, 15:14:03 replay,
   15:14:09 turn 2). Source (`internal/mcp/server.go:105` @4cb09fb) logs the MCP call outcome after
   `SendWithRunner`, so an idempotent replay is also logged as accepted. Native history (one turn-1 user
   message) is the authoritative duplicate check. P3 audit improvement: distinguish new submission from
   replay in the log.

## Operator records (2026-11-03, after the run)
- Bridge PID 2682513, ActiveEnter 15:13:02Z, NRestarts=0, exe sha256 05000a48…fd73;
  `BRIDGE_TOKEN_EXPIRES=2026-11-04T11:56:12Z`.
- OpenCode PID 2681945, ActiveEnter 15:11:53Z.
- Bridge journal 15:13–15:15: 17 `boundary_allowed=true`, 0 `boundary_allowed=false`, 3 `prompt submission accepted=true`.

## Independent review (Worker C, R3A-REVIEW-01, 2026-11-03)
Native SQLite snapshot (db sha256 3deb840b…f416, wal c5ad8f1a…c488; source unchanged during copy):
23 messages; the four new IDs, parents 21→20 and 23→22, finish stop and completed times, turn-1 text in
exactly one user message, turn-2 user text without the marker, recall in #23, model openrouter /
z-ai/glm-5.3-flash for #19–23 — all PASS. Bridge/OpenCode PIDs and log model lines — PASS.
Formal disposition REVIEW_FAIL only because the evidence file had not been placed on the host.
Reviewer's open question on the 15:14:03 submission line and binary provenance is resolved by:
the running binary's `go version -m` reports vcs.revision 4cb09fb…, vcs.modified=false (R3A-PREP-01);
at 4cb09fb `internal/mcp/server.go:105` logs only `accepted`, while `main` (H2, line 97) also logs
session_id/execution_id — the reviewer compared against `main`. The H3 change removed execution
identifiers from this log line; restoring a non-secret execution ID plus a replay flag is the P3
audit item in finding 6. Formal re-review against the placed document is pending.

## Review outcome (R3A-REVIEW-01b, Worker C, against document sha256 b84a4c73…f062)
REVIEW_PASS on every criterion measurable from native storage, journal and binary metadata: identity/cwd,
turn 1 and 2 completion and parents, marker recall, +2/+2 messages, replay with zero native effect
(code path `internal/execution/manager.go:146` @4cb09fb returns the existing snapshot for same key and
payload), same writer PID, model, auth-negative journal lines, operator counts, binary provenance
(`vcs.revision` 4cb09fb, modified=false, hash equals /proc/2682513/exe).
Client-observed only (UNVERIFIED independently because H3 does not log them): FORBIDDEN probe result,
SESSION_BUSY error code and key, HTTP status codes of the auth negatives, execution IDs c76148d6/d846680d.
Reviewer added: first-19-ID list sha256 `223de436…41f9` for future comparison; PID 2652081 (added above).

## Planning Desk verdict
**R3a: PASS (Phase 4 gap closure)** — real Claude Chat → authenticated connector → existing OpenCode
session, two turns with marker recall, same-key replay without native duplication, busy rejection,
uncertainty handling and auth negatives, with independent native corroboration. Limits: ADR-011 live late
completion NOT_EXERCISED; client-observed error codes not independently logged (P3 audit item).

## Open items
- Maintainer: connector removal now or token expiry at 2026-11-04T11:56:12Z.
- Backlog: P3 audit logging (execution ID, replay flag, error code); R5 "native stalled" liveness.
