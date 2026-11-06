# R5b evidence: live permission-wait detection (2026-11-06)

Planning Desk, 2026-11-06. Supplementary to `R5_EVIDENCE_2026-11-05.md`, and run under `R5B_RUNBOOK.md`.

## Runtime
| Item | Value |
|---|---|
| Writer | `/usr/local/libexec/harness-bridge-r5`, sha256 `d98373342fd27ceda9eef72afe4713aa2929cc0b87abbb66ea934409eeed63b7`, built from main `692992dbf78d72861db989ee7b4b1c580795b1c5` (unchanged since R5) |
| Writer PIDs | 3181516 (after the token rotation at 08:56Z), then 3217118 (after the post-test restart; exe sha256 re-verified as above) |
| Token | rotated 2026-11-06 08:56Z, `BRIDGE_TOKEN_EXPIRES=2026-11-07T06:56:41Z`; connector "Harness Bridge R5" re-added |
| OpenCode | 1.19.4, `opencode-hb-test.service`, 127.0.0.1:4097; target `opencode:ses_e9329c0b4ffebOlm7GRyKxbMQ5`, workspace `/opt/harness-bridge-test/workspace` |
| Permission policy | drop-in `zz-r5b-permission.conf` with `OPENCODE_PERMISSION={"bash":"ask"}`. The variable was verified in the environment of the opencode process (pid 3181830) before the test. |

## Pre-test blocker and native recovery (finding F-6)
1. P1 (`r5b-p1-…`, idem prefix `8a3bf8f6a6b9`) was rejected at 09:04:45Z with `SESSION_BUSY: unfinished native assistant message`. Nothing was submitted.
2. Cause: R5 Step 4 restarted OpenCode while a tool call was running. That restart left an orphan assistant message, `msg_1ad247bd90019VrZ7VL7b4QgWO`, with no completion time.
3. The maintainer called native abort (`POST /session/:id/abort`). It returned HTTP 200 but had no effect, because there is no runner and `cancel` only sets idle (`run-state.ts:76-85`).
4. The maintainer then ran native `DELETE /session/:id/message/msg_1ad247bd90019VrZ7VL7b4QgWO`. The orphan was removed.
5. The unanswered R5 Step 4 user message `msg_1ad24788c…` remains in history. It is harmless and was left untouched.
6. Discovery reported the session as `idle` during the blocker while admission refused it (finding F-5).

## Step 3: conversation (P1b onward)
| Seq | Tool | Key arguments | Result (tool time, UTC) |
|---|---|---|---|
| b5 | send_prompt | key `r5b-p1b-a53049e7ca76dcc8` | exec `925e32932549e973f054fd2713d1a6d5`, msg `msg_1b020361100018e6beb0dfab87`, `submitting`, 09:11:03.44Z |
| b6 | get_execution_status | `925e3293…` | `running`, 09:11:07.00Z |
| b7 | get_execution_status | `925e3293…` | **`waiting_for_permission`**, 09:11:10.49Z |
| b8 | get_session | target | **`waiting_for_permission`**, native `busy` (observed, not inferred) |
| b9 | read_session_history | limit 3 | bash part pending, input `echo R5B-PERM-PROBE` |
| — | human (native HTTP API) | `GET /permission` → `per_1b0204481001EoTKc1sL8T4XGf` (bash, pattern `echo R5B-PERM-PROBE`); `POST /session/:id/permissions/per_1b02…` `{"response":"reject"}` | native bash part `status: error` at 09:13:15.22Z |
| b10–b50 | get_execution_status | `925e3293…`, about every 3 s | `unknown`, `UNKNOWN_OUTCOME: native status absent or unsupported without terminal proof`, `OBSERVATION_UNAVAILABLE` since 09:13:15.48Z |
| b51–b59 | get_execution_status | `925e3293…` | `unknown`, `UNKNOWN_OUTCOME: native terminal evidence remains inconclusive`, `MONITOR_EXPIRED` since 09:16:03.44Z |
| b61 | get_execution_result | `925e3293…` | `unknown` / `MONITOR_EXPIRED`, 09:16:41.25Z. The b60 call was cut off client-side; it was read-only and was retried. |
| b62 | get_session | target | `unknown` (guard held), native `idle` (inferred from absence) |
| b63 | read_session_history | limit 10 | no new messages after the rejection |

**Native record of the rejection**
- Assistant message `msg_1b0203a3d001MIfPp3NKHCpELy` completed at 09:13:15.25Z with `finish: tool-calls`.
- The bash part has `status: error` and the message "The user rejected permission to use this specific tool call." There is no output and no exit code.
- The agent took no further step after the rejection.

**Probe:** `R5B-PERM-PROBE` appears only in the prompt, the tool input and the agent's reasoning. It never appears in any tool output.

## Step 4: revert and corroboration
1. `zz-r5b-permission.conf` was removed, then `daemon-reload` was run and `opencode-hb-test` restarted. `OPENCODE_PERMISSION` count in the opencode process environment: **0**.
2. The Bridge was restarted to release the guard held on `unknown` (ADR-010). New PID 3217118, exe sha256 `d9837334…63b7`.
3. Journal since 08:56:00Z (`event=(prompt_submission|interrupt)`): exactly two lines:
   ```
   09:04:45 prompt_submission outcome=rejected accepted=false error_code=SESSION_BUSY session_id=opencode:ses_e9329c0b4ffebOlm7GRyKxbMQ5 idem=8a3bf8f6a6b9 principal=operator
   09:11:03 prompt_submission outcome=new accepted=true session_id=opencode:ses_e9329c0b4ffebOlm7GRyKxbMQ5 execution_id=925e32932549e973f054fd2713d1a6d5 idem=acce088fd930 principal=operator
   ```
   There is exactly one `new` line, for P1b, and no interrupt. Only 12-hex key prefixes are logged.
4. Post-restart `get_session`: `state: idle`, `native_state: idle`, `native_state_inferred_from_absence: true`.
5. Native DB corroboration (runbook Step 4.3): **PASS**. Worker B (#76, independent of the run) checked a read-only snapshot taken at 09:33:44Z (`opencode.db` sha256 `5811d84e…21ef`):
   - The orphan `msg_1ad247bd90019VrZ7VL7b4QgWO` is absent (0 message rows, 0 part rows). Its parent user message `msg_1ad24788c000e0cd7c8dbc80dd` is still present.
   - Exactly one new user message, `msg_1b020361100018e6beb0dfab87` (09:11:03.507Z), maps 1:1 to the single `new` journal line. The rejected P1 added none.
   - Assistant `msg_1b0203a3d001MIfPp3NKHCpELy` completed at 09:13:15.250Z with `finish: tool-calls`. Its bash part has `status: error`, the rejection message, input `echo R5B-PERM-PROBE`, and no `output` key. No assistant message follows it.
   - `R5B-PERM-PROBE` appears in no tool output, metadata, title or assistant text.
   - OpenCode does not persist permission replies: the `permission` table has 0 rows and there are no permission event rows. The tool part error is the only native evidence of the rejection.
   - The workspace is unchanged since R5: only the three `r5/` files exist beyond the R3a baseline, with the same hashes as #73.

## Verdict
| Criterion | Result |
|---|---|
| `waiting_for_permission` observed on the execution while pending | PASS (b7) |
| `waiting_for_permission` observed on the session (`get_session`) | PASS (b8, native `busy`) |
| The Bridge made no approval | PASS (no Bridge permission tool exists; the journal shows no other mutation) |
| The human rejected natively | PASS (`per_1b0204481001EoTKc1sL8T4XGf`, reject) |
| Post-rejection state is terminal or `unknown`, never success | PASS (`unknown`, then `MONITOR_EXPIRED`) |
| `R5B-PERM-PROBE` never produced | PASS |
| Guard consistent with the state | PASS (held on `unknown`; released only by the Bridge restart) |
| Journal corroborates | PASS |
| Native DB corroborates | PASS (#76) |

**R5b: PASS. MVP accepted on revision `692992d`, including R5b.** The single R5 gap, live permission-wait detection, is closed, and every pass criterion is corroborated by the journal and by native records.

## Findings (none blocks the MVP; all fail safe)
| ID | Finding | Severity |
|---|---|---|
| F-5 | Discovery reports `idle` while admission refuses with `SESSION_BUSY: unfinished native assistant message`. Discovery does not consider unfinished assistant messages. | P2 |
| F-6 | An OpenCode restart during a tool call leaves an orphan assistant message. Native abort cannot clear it, and the only recovery is native `DELETE /session/:id/message/:messageID`. | P2 |
| F-7 | A native permission rejection is not mapped to a terminal state. The execution stays `unknown` (`OBSERVATION_UNAVAILABLE` → `MONITOR_EXPIRED`) and the guard stays held. Upstream signals must be verified before a typed terminal outcome (e.g. `failed` / `PERMISSION_REJECTED`) is proposed. | P2 |
| P3-perm | Bridge status does not expose which permission is pending (tool, pattern). | P3 |
| known | The guard is held on `unknown` until a Bridge restart (ADR-010). | P2 (known) |

## Limitations
- There is no exact Bridge timestamp for the rejection. The first post-rejection poll was at 09:13:30Z; the transition time comes from the native record (09:13:15.22Z) and the Bridge `since` field (09:13:15.48Z).
- The writer is `692992d`. It predates F-3 bounding (#55, #67) and the ADR-017 (a) liveness reporting (#56). F-7 behavior must be re-checked on current main.
- ADR-011 late completion and client timeout remain NOT_EXERCISED, as in R5.
