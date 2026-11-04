# R3b evidence: integrated `main` in read-only mode through the real connector

Date: 2026-11-04 (UTC). Executed from the Planning Desk Claude conversation through the custom connector
"Harness Bridge" (static Request header, Max plan).
Status: **PASS with findings** (see verdict).

## Runtime (operator records, 2026-11-03 ~22:18Z)
- **Binary:** `/usr/local/libexec/harness-bridge-r3b`, sha256 `f8826995149a347aee4123057ceb8153cca6e24c1c8edbe84bbff8c40b7bb7e6`.
  - Built by Worker C (R3B-BUILD) from `main` `9ed25a377cd6af768ed2947d44df8642c2324a30`, `vcs.modified=false`.
- **Process:** PID 3089563, MainPID = ExecMainPID, `NRestarts=0`, ExecMainStart 22:18:32Z.
  - `/proc/3089563/exe` points to the r3b path; its sha256 equals the file hash above.
  - Listening on 172.18.0.1:8443 (fd 3).
  - The rollback binary `/usr/local/libexec/harness-bridge-private` is untouched (sha256 `05000a48…fd73`).
- **Configuration:** drop-in `zz-r3b.conf` (ExecStart override plus `BRIDGE_READ_ONLY=1`).
  - The environ has exactly one `BRIDGE_READ_ONLY` entry; its value was not printed.
  - Read-only mode is proven behaviorally (calls 13–15).
- **First diagnostic anomaly (explained):** the first operator check reported exe hash `8390bf37…` and no `BRIDGE_READ_ONLY` line. A re-check of the same PID reported the correct hash and one entry.
  - Cause: `8390bf37…` is the sha256 of `/usr/lib/systemd/systemd-executor` (systemd 255.4-1ubuntu8.18). It was read before the executor `execve`'d the service binary. The same reading recurred right after the rollback restart.
  - Rule: record `/proc/<pid>/exe` only after `ls -l` shows the target binary path; otherwise re-read.
  - No Chat tool call was made until the re-check passed.
- **Target:** OpenCode 1.19.4 test instance `127.0.0.1:4097`, session `opencode:ses_e9329c0b4ffebOlm7GRyKxbMQ5`.

## Call log
| # | Tool | Arguments | Result |
|---|---|---|---|
| 0 | (client) RefreshMcpTools | — | 7 tools (client cache) |
| 1 | get_session | ses_unauthorized_probe | FORBIDDEN "session not authorized", retryable=false |
| 2 | list_sessions | — | 1 session. New fields `harness_instance_id`, `native_project_id: global`. `state: unknown`, `native_state` absent |
| — | (client) tool list update | — | 4 new tools appeared (`find_sessions`, `get_harness_capabilities`, `get_repository`, `list_repositories`) plus new server instructions. 11 tools total |
| 3 | get_harness_capabilities | target | primary_agents [build, plan], subagents [explore, general], native_session_creation true, bridge_session_creation false, native_delegation unknown, child_session_inspection true |
| 4 | list_repositories | — | `[]` (the session's project is `global`, so there is no repository identity) |
| 5 | get_session | target | identity and cwd `/opt/harness-bridge-test/workspace` unchanged; `state: unknown` |
| 6 | get_harness_capabilities | ses_unauthorized_probe | FORBIDDEN |
| 7 | find_sessions | limit 1, offset 0 | 1 session |
| 8 | find_sessions | limit 1, offset 1 | `[]` (pagination boundary correct) |
| 9 | find_sessions | limit 0 | 1 session (0 = default 50, as in the code; the description says 1–200) |
| 10 | get_repository | `repo:does-not-exist` | NOT_FOUND "repository not visible" |
| 11 | read_session_history | target (legacy form) | 23 messages; IDs and order identical to the R3a end state |
| 12 | send_prompt | ses_unauthorized_probe | FORBIDDEN "mutations disabled on this bridge" (the read-only gate runs before authorization) |
| 13 | get_execution_status | nonexistent ID | NOT_FOUND |
| 14 | get_execution_result | nonexistent ID | NOT_FOUND |
| 15 | interrupt_execution | nonexistent ID | FORBIDDEN "mutations disabled on this bridge" |
| 16 | send_prompt | target, key `r3b-ro-probe-3f8a0c6e19` | FORBIDDEN "mutations disabled on this bridge" |
| 17 | read_session_history | target | still 23 messages; last ID `msg_1a1f98fe2001ytazuFDcLzVSNV`. Zero native effect |

## Verdict per criterion
| Criterion | Result |
|---|---|
| Eleven-tool discovery through the real connector | PASS (after the client tool-list update) |
| Seven legacy tools still callable with legacy arguments | PASS (all seven called) |
| Read-only: `send_prompt` and `interrupt_execution` rejected, zero native effect | PASS (calls 12, 15, 16, 17) |
| Unauthorized session → typed FORBIDDEN on old and new tools | PASS (calls 1, 6) |
| `find_sessions` pagination | PASS (calls 7–8) |
| `get_repository` negative | PASS (call 10) |
| `get_repository` / `list_repositories` positive path | NOT_EXERCISED (the test session has no git repository) |
| `read_session_history` pagination (`limit`/`before`) | NOT_EXERCISED: this client kept the cached legacy schema (`session_id` only) for the seven pre-existing tools |
| Session state `idle` for an idle native session | **FAIL → finding F-4** |

## Findings
- **F-4 (P2, functional, fail-safe direction): discovery never reports `idle` on OpenCode 1.19.4.**
  - Native behavior: OpenCode v1.19.4 `packages/opencode/src/session/status.ts` deletes the session from the status map on `idle` (`data.delete(sessionID)`). `get()` returns `{type:"idle"}` for absent sessions. So `/session/status` lists only non-idle sessions.
  - Bridge behavior: `internal/mcp/discovery.go` (`view`, at `9ed25a3`) maps only an explicit `"idle"` entry to `idle`, so an idle session is reported `unknown` with an empty `native_state`.
  - Inconsistency: `internal/execution/reconcile.go:285` already treats a missing status (`!exists`) as idle-equivalent.
  - Effect: the state is safe (never falsely idle), but the MVP state model cannot show `idle`, and a supervisor cannot tell idle from unknown.
  - This blocks H4-A promotion; it does not block R3b.
  - Fix candidate (FIX-STATE-01): when `statusOK`, `permissionsOK`, the directory matches and the session is absent, report `idle`, with `native_state: "idle (absent)"` or an equivalent evidence flag. The guard overlay continues to win. Tests must use the real native shape (an empty map).
- **F-5 (P3, docs):** the `find_sessions` description says "limit 1–200", but `limit: 0` is accepted as the default of 50.
- **F-6 (P3, client behavior, not a Bridge defect):** after a tool-set change, the claude.ai client updated the tool list but kept the cached input schemas of the existing tools. Acceptance runs that need new arguments on existing tools need a fresh conversation, or removing and re-adding the connector.

## Planning Desk verdict
**R3b: PASS for the roadmap exit criterion (eleven-tool discovery, seven-tool compatibility), with F-4 recorded as an H4-A promotion blocker.** Read-only is proven by behavior with zero native effect.
Limits: history pagination and the positive repository path were NOT_EXERCISED.
This was a Planning Desk run, not an independent review. Corroboration with the operator journal is optional (boundary counts for the window, no mutation log lines).

## Closure
- **Rollback (operator, done):** `zz-r3b.conf` removed; drop-ins now only `90-h3-writer.conf`.
  - PID 3169349, ExecMainStart 2026-11-03 22:32:56Z, `NRestarts=0`, active.
  - `/proc/3169349/exe` → `/usr/local/libexec/harness-bridge-private`, sha256 `05000a48…fd73` (H3 `4cb09fb`).
  - `BRIDGE_READ_ONLY=0` (writer mode restored).
  - No mutation is planned through the connector until a new writer candidate passes its own acceptance.
- The token expires at 2026-11-04T11:56:12Z.
