# Phase 6 Claude Code minimal spike: evidence (2026-11-10)

Source plan: `docs/research/P6_CLAUDE_CODE_FEASIBILITY.md` §9 "Minimal spike plan", steps 1-8.
Task: issue #140 (replaces #136). Revised under issue #143 to address review #142 (P2-1, P2-2, P3-1..4). Evidence only; no Go code, ADR or SPEC changed.

## Environment

| Item | Value |
|---|---|
| Host arch | aarch64 (Linux 6.8) |
| CLI | `hb-p6-claude --version` -> `2.1.322 (Claude Code)`; `system/init.claude_code_version` = `2.1.322` |
| Auth mode | subscription OAuth via wrapper `hb-p6-claude` (runs `claude` as non-root user `hbspike`). `system/init.apiKeySource` = `none`. Credentials were never read. |
| Model | `claude-sonnet-5-5` (`--model sonnet` on every run) |
| `permissionMode` | `default` in every `system/init` observed (flags `--permission-mode default --permission-prompts none` on every stream-json evidence run; the plain `say ok` smoke in Preconditions ran without those flags) |
| `init.capabilities` | `interrupt_receipt_v1, interrupt_cancel_queued_v1, interrupt_send_now_v1, interrupt_requeue_taken_v1, msg_lifecycle_v1, request_marker_lists_v1, ask_user_question_unanswered_v1, lifecycle_wakeup_source_v1, sdk_mcp_tools_list_changed, sdk_mcp_manifests, mcp_read_resource_v1, mcp_tool_ui_meta_v1, ui_surface_v1, mcp_call_result_not_for_model_v1, mcp_call_check_permissions_v1` |
| Project dirs | `/opt/developer/hb-work/a/p6-spike/proj-x`, `.../proj-y` (empty, no hooks/.mcp.json/CLAUDE.md, outside the repo) |
| Runs | 23 of the 25 allowed (17 original + 6 re-runs for #142; see Limitations for the list); every run `--max-turns` <= 5; never `--bare`/`auto`/`bypassPermissions` |
| Cumulative reported `total_cost_usd` | about 2.5 upper bound (original about 1.4: sum of `result` lines I could read, 1.33 plus two runs without a result line or not recorded; re-runs for #142: about 1.09, the sum of the six reported `total_cost_usd` values; notional on a subscription; the cost field is cumulative per session on resumed sessions, so the sum overcounts). Well under the 5 cap. |

Notes common to all runs: stdin was redirected from `/dev/null` (otherwise the CLI prints `Warning: no stdin data received in 3s`). stderr also carried one `[mcp-sdk] SEP-2352 ... stored OAuth credential has no 'issuer' stamp` warning on the first plain run (no value shown).

## Preconditions

1. `hb-p6-claude --version` -> `2.1.322 (Claude Code)`, exit 0.
2. In proj-x: `hb-p6-claude -p "say ok" --max-turns 1 --model sonnet` -> stdout `ok`, exit 0. Then the same prompt with `--permission-mode default --permission-prompts none --output-format stream-json --verbose`, exit 0:
   `system/init`: `apiKeySource:"none"`, `claude_code_version:"2.1.322"`, `model:"claude-sonnet-5-5"`, `permissionMode:"default"`, `cwd` = proj-x. `result`: `subtype:"success"`, `is_error:false`, `terminal_reason:"completed"`.

## Verdict table

| Step | Topic | Verdict |
|---|---|---|
| 1 | Version audit | OBSERVED-AS-EXPECTED |
| 2 | Exact-ID resume (dir X and dir Y) | OBSERVED-AS-EXPECTED, with one finding: resume from Y runs in cwd Y |
| 3 | Result correlation, induced failure | OBSERVED-AS-EXPECTED |
| 4 | Unknown ID | OBSERVED-AS-EXPECTED (no side effects observed) |
| 5 | Concurrency | native half OBSERVED-DIFFERENT: both runs accepted; one turn absent from later history **(inferred** from the model's own history listing; seen in the original run and the #142 re-run, a different turn each time**)**; Bridge-side single-writer half NOT_EXERCISED |
| 6 | Interrupt (SIGINT, SIGTERM) | OBSERVED-DIFFERENT in two points (see below); Bridge `UNKNOWN_OUTCOME` half NOT_EXERCISED |
| 7 | Permission fail-closed | OBSERVED-AS-EXPECTED |
| 8 | Cross-project denial | NOT_EXERCISED (no Claude Code adapter/spawn path exists yet) |

## Step 1: version audit

Raw `system/init` line (redacted) from the #142 re-run, see the Step 2 re-run block (`r1create`). The original stream-json raw output was not retained.

See Environment. `claude_code_version` equals the CLI `--version`; auth mode `apiKeySource:"none"` (subscription OAuth). Capabilities list above.

## Step 2: exact-ID resume

Original runs (raw output not retained; the values below were read from the outputs at the time and the re-run block below gives raw excerpts). Session A created in proj-x (run 1), marker `PELICAN-7731`, prompt 2 does not repeat it.

- Create: `hb-p6-claude -p "Remember the marker word PELICAN-7731. Reply with only: stored." --model sonnet --max-turns 1 --permission-mode default --permission-prompts none --output-format stream-json --verbose`; exit 0; `init.session_id` = `ca76dc7f-09fb-4736-b2bd-48f8a2ee6df5`, `init.cwd` = proj-x; `result.result` = `stored.`.
- Resume from X: `... --resume A "What was the marker word? Reply with only the word."` exit 0; `init.session_id` = A (same), `init.cwd` = proj-x, `result.result` = `PELICAN-7731`.
- Resume from Y: same command with cwd proj-y; exit 0; `init.session_id` = A (same), **`init.cwd` = proj-y**, `result.result` = `PELICAN-7731`. proj-x and proj-y gained no files from this.

Finding: `--resume A` is not bound to the creating directory; the cwd used is the invoking directory. A Bridge that must pin a session to a project has to enforce that itself (matches the step 8 expectation).

### Re-run for #142 (2026-11-10): raw excerpts for steps 1 and 2

Fresh session `4e59c1bd-4333-4a71-875d-cba9301bd4a9`, marker `HERON-5520`, same flags as above (`--model sonnet --permission-mode default --permission-prompts none --output-format stream-json --verbose --max-turns 1`, stdin `/dev/null`). Exit codes captured by the shell. Redaction: usage, tools, skills, socket paths, uuids and credential-expiry fields removed; remaining fields verbatim. These sit beside, not in place of, the original observations.

Create in proj-x (`exit=0`; this run also serves as the step 1 stream-json audit):
```
{"type": "system", "subtype": "init", "cwd": "/opt/developer/hb-work/a/p6-spike/proj-x", "session_id": "4e59c1bd-4333-4a71-875d-cba9301bd4a9", "model": "claude-sonnet-5-5", "permissionMode": "default", "apiKeySource": "none", "claude_code_version": "2.1.322", "original_cwd": "/opt/developer/hb-work/a/p6-spike/proj-x"}
{"stop_reason": "end_turn", "session_id": "4e59c1bd-4333-4a71-875d-cba9301bd4a9", "total_cost_usd": 0.0896402, "permission_denials": [], "terminal_reason": "completed", "is_error": false, "num_turns": 1, "subtype": "success", "result": "stored.", "type": "result"}
```

Resume from proj-x (`exit=0`):
```
{"type": "system", "subtype": "init", "cwd": "/opt/developer/hb-work/a/p6-spike/proj-x", "session_id": "4e59c1bd-4333-4a71-875d-cba9301bd4a9", "model": "claude-sonnet-5-5", "permissionMode": "default", "apiKeySource": "none", "claude_code_version": "2.1.322", "original_cwd": "/opt/developer/hb-work/a/p6-spike/proj-x"}
{"stop_reason": "end_turn", "session_id": "4e59c1bd-4333-4a71-875d-cba9301bd4a9", "total_cost_usd": 0.1742537, "permission_denials": [], "terminal_reason": "completed", "is_error": false, "num_turns": 1, "subtype": "success", "result": "HERON-5520", "type": "result"}
```

Resume from proj-y (`exit=0`):
```
{"type": "system", "subtype": "init", "cwd": "/opt/developer/hb-work/a/p6-spike/proj-y", "session_id": "4e59c1bd-4333-4a71-875d-cba9301bd4a9", "model": "claude-sonnet-5-5", "permissionMode": "default", "apiKeySource": "none", "claude_code_version": "2.1.322", "original_cwd": "/opt/developer/hb-work/a/p6-spike/proj-y"}
{"stop_reason": "end_turn", "session_id": "4e59c1bd-4333-4a71-875d-cba9301bd4a9", "total_cost_usd": 0.18209720000000001, "permission_denials": [], "terminal_reason": "completed", "is_error": false, "num_turns": 1, "subtype": "success", "result": "HERON-5520", "type": "result"}
```

Result: agrees with the original. Same `session_id`, history retained (marker recalled without being in the prompt), `init.cwd` follows the invoking directory (proj-y on the third run). Verdict unchanged.

## Step 3: result correlation

Every run above produced exactly one `type:"result"` line per turn, `subtype:"success"`, `is_error:false`, `terminal_reason:"completed"`, exit 0, `session_id` equal to the init `session_id`.

Induced failure, proj-x, fresh session `ef86269d-34d5-4bea-8d03-c19efc985653`, `--max-turns 1`, prompt: read a.txt then b.txt then report both: exit **1**;
`{"type":"result","subtype":"error_max_turns","is_error":true,"num_turns":2,"terminal_reason":"max_turns","stop_reason":"tool_use","permission_denials":[],"errors":["Reached maximum number of turns ..."]}`.
Note `num_turns` is 2 for `--max-turns 1`, and `result.result` is absent.

## Step 4: unknown ID

`hb-p6-claude -p "hi" --resume 00000000-0000-4000-8000-000000000000 --max-turns 1 ...` -> exit **1**, stderr `No conversation found with session ID: 00000000-0000-4000-8000-000000000000`. stdout is a single line, no `system/init`:
`{"type":"result","subtype":"error_during_execution","duration_ms":0,"is_error":true,"num_turns":0,"stop_reason":null,"session_id":"00000000-0000-4000-8000-000000000000","total_cost_usd":0,"permission_denials":[],"errors":["No conversation found with session ID: 00000000-0000-4000-8000-000000000000"]}`.
Side effects: cost 0, no tokens, proj-x directory listing unchanged (only a.txt, b.txt). The result echoes the requested ID as `session_id`; no new session is created. Not checked: whether a transcript file appeared under the `hbspike` home (credentials area, deliberately not read), so "zero side effects" is limited to the project dir and the reported cost.

## Step 5: concurrency

Two concurrent `-p --max-turns 1 --resume A` from proj-x (`"Reply with only: first."` and `"... second."`), started together via the shell `&`:
both exited 0, both `init.session_id` = A, both `result.subtype:"success"` with `result` = `first.` / `second.`. No refusal, no lock error, no stderr.

Original follow-up run listing user messages in order (raw output not retained) reported that the history afterwards contained `... second.` but not `Reply with only: first.`. **(inferred)**: this is the model's own account of its history, not a transcript read, so "one turn lost; last writer wins" is an inference.
Verdict (native half): OBSERVED-DIFFERENT from "refusal": the CLI neither serialises nor rejects; the history listing indicates one branch is lost **(inferred)**.
Bridge half ("fence rejects the second Bridge writer"): **NOT_EXERCISED**. No Claude Code adapter/spawn path exists yet; the OpenCode path's analogues are the operator session ACL (ADR-012), SESSION_BUSY / the native-session flock (ADR-010) and `UNKNOWN_OUTCOME`. Design expectation: the Bridge admits one writer per session and returns a conflict to the second before spawning. The native result above is the argument for needing it.

### Re-run for #142 (2026-11-10): raw excerpts for step 5

Same session `4e59c1bd-4333-4a71-875d-cba9301bd4a9`, three runs from proj-x: two concurrent `--resume` (shell `&`, then `wait`) and one history listing afterwards. Same flags, `--max-turns 1`.

Concurrent run a, prompt `Reply with only: first.` (`exit=0`):
```
{"type": "system", "subtype": "init", "cwd": "/opt/developer/hb-work/a/p6-spike/proj-x", "session_id": "4e59c1bd-4333-4a71-875d-cba9301bd4a9", "model": "claude-sonnet-5-5", "permissionMode": "default", "apiKeySource": "none", "claude_code_version": "2.1.322", "original_cwd": "/opt/developer/hb-work/a/p6-spike/proj-x"}
{"stop_reason": "end_turn", "session_id": "4e59c1bd-4333-4a71-875d-cba9301bd4a9", "total_cost_usd": 0.21069600000000002, "permission_denials": [], "terminal_reason": "completed", "is_error": false, "num_turns": 1, "subtype": "success", "result": "first.", "type": "result"}
```

Concurrent run b, prompt `Reply with only: second.` (`exit=0`):
```
{"type": "system", "subtype": "init", "cwd": "/opt/developer/hb-work/a/p6-spike/proj-x", "session_id": "4e59c1bd-4333-4a71-875d-cba9301bd4a9", "model": "claude-sonnet-5-5", "permissionMode": "default", "apiKeySource": "none", "claude_code_version": "2.1.322", "original_cwd": "/opt/developer/hb-work/a/p6-spike/proj-x"}
{"stop_reason": "end_turn", "session_id": "4e59c1bd-4333-4a71-875d-cba9301bd4a9", "total_cost_usd": 0.18621600000000002, "permission_denials": [], "terminal_reason": "completed", "is_error": false, "num_turns": 1, "subtype": "success", "result": "second.", "type": "result"}
```

History listing, prompt `List, in order, every user message in this conversation after the marker, one per line, verbatim.` (`exit=0`), quoted result:
```
{"type": "system", "subtype": "init", "cwd": "/opt/developer/hb-work/a/p6-spike/proj-x", "session_id": "4e59c1bd-4333-4a71-875d-cba9301bd4a9", "model": "claude-sonnet-5-5", "permissionMode": "default", "apiKeySource": "none", "claude_code_version": "2.1.322", "original_cwd": "/opt/developer/hb-work/a/p6-spike/proj-x"}
{"stop_reason": "end_turn", "session_id": "4e59c1bd-4333-4a71-875d-cba9301bd4a9", "total_cost_usd": 0.24553740000000002, "permission_denials": [], "terminal_reason": "completed", "is_error": false, "num_turns": 1, "subtype": "success", "result": "What was the marker word? Reply with only the word.\nWhat was the marker word? Reply with only the word.\nReply with only: first.\nList, in order, every user message in this conversation after the marker, one per line, verbatim.", "type": "result"}
```

Result: both concurrent runs accepted (exit 0, no refusal, empty stderr). The listing contains `first.` but **not** `second.`, so one turn was again missing, but a different one from the original run (original: `first.` missing). The listing still shows each earlier turn once; the model's listing is its own account of the loaded history, so the loss remains **(inferred)**, not read from a transcript. Both runs agree on "no refusal and one concurrent turn absent from later history"; which turn survives is not stable (race), so "last writer wins" is NOT established. Verdict unchanged: native half OBSERVED-DIFFERENT; Bridge half NOT_EXERCISED.

## Step 6: interrupt

Tool call: Bash `python3 -c 'import time; time.sleep(80)'` allowed with the scoped `--allowedTools "Bash(python3 *)"` (not a permission bypass; `permissionMode` stayed `default`). Because the tool was auto-allowed by that scoped rule, step 6 covers interrupting an allowed tool, not one that would have prompted. Resumed session A, `--max-turns 3`. The signal was sent to the `claude` child (found with `pgrep -P <wrapper pid>`; the wrapper is `sudo` whose child is `/home/<spike-user>/.local/bin/claude -p ...`) 4 s after the first `tool_use` appeared on stdout. Failed first attempts (a `sleep` is blocked by the CLI and backgrounded; a `tail /dev/null` is outside the project dir and denied) did not exercise the signal and are not counted as evidence.

- Exit codes below are the wrapper exit code from the original run; the raw shell capture was not retained and step 6 was not re-run (budget).
- **SIGINT**: wrapper exit **0** (wrapper exit code from the original run, raw shell capture not retained). stdout ends with a `user` message `[Request interrupted by user for tool use]` and then
  `{"type":"result","subtype":"error_during_execution","is_error":true,"num_turns":3,"terminal_reason":"aborted_tools","stop_reason":"tool_use","permission_denials":[]}`.
  Resume afterwards (exit 0) answered that the command "never ran, the tool use was rejected before it started", i.e. the interrupted tool call is recorded as rejected, session still usable.
- **SIGTERM**: wrapper exit **143** (wrapper exit code from the original run, raw shell capture not retained); stdout stops after `system/task_started`, `system/task_notification` and a `user` line; **no `result` line**. Resume afterwards (exit 0) worked; the model reported the command "exited with code 137 ... terminated partway through the 80-second sleep". The session is resumable and the killed tool call is recorded.
- The Bridge `UNKNOWN_OUTCOME` mapping for SIGTERM: **NOT_EXERCISED** (no Claude Code adapter/spawn path exists yet; the OpenCode analogue is `UNKNOWN_OUTCOME`). Expectation: exit 143 with no `result` line maps to `UNKNOWN_OUTCOME`.

OBSERVED-DIFFERENT points: (a) SIGINT exit code is 0 although `is_error` is true; (b) an interrupted tool call is recorded in the transcript inconsistently between SIGINT ("rejected, never ran") and SIGTERM ("killed, code 137").

## Step 7: permission fail-closed

proj-x, fresh session `3142d6ed-05d0-4fe8-84dd-4fed7eed2f87`, `--permission-mode default --permission-prompts none`, prompt: Write `note.txt`. Exit 0, `permissionMode:"default"`. Nothing was approved; `note.txt` was not created (directory listing: a.txt, b.txt only).
`result.permission_denials`: `[{"tool_name":"Write","tool_use_id":"toolu_01X7Cs3hZdn7PeqY61Y6AwY3","tool_input":{"file_path":"/opt/developer/hb-work/a/p6-spike/proj-x/note.txt","content":"hello"}}]`, `subtype:"success"`, `is_error:false`. The model told the user the Write was denied automatically. Note: a denial does not make the run an error; callers must read `permission_denials`.

## Step 8: cross-project denial

**NOT_EXERCISED.** No Claude Code adapter/spawn path exists yet; the OpenCode path's analogue is the operator session ACL (ADR-012) checked before use. Design expectation: a session ID outside the caller's project ACL is refused by the Bridge before any `claude` spawn, with no process started. Step 2 shows the CLI itself would resume it in whatever cwd it is given.

## Limitations

- 23 `hb-p6-claude` runs (17 original + 6 for #142): version check excluded; plain `say ok`, stream-json `say ok`, create A, resume X, resume Y, max_turns failure, unknown ID, concurrent a, concurrent b, history listing, failed SIGINT attempt (`sleep` blocked), failed SIGINT attempt (`tail` denied), SIGINT run, resume after SIGINT, SIGTERM run, resume after SIGTERM, Write denial; re-run for #142: create (stream-json init), resume X, resume Y, concurrent a, concurrent b, history listing.
- Single model (sonnet), single host, one CLI version; subscription rate limits not probed.
- Step 5 interleaving was inferred from the model's own report of the history, not from reading the transcript file (not read: it lives under the `hbspike` home with credentials).
- Step 6 timing: the signal was sent 4 s after the tool call started; other timings were not tried. Exit code 0 for SIGINT is of the `hb-p6-claude`/`sudo` wrapper; it may differ from the bare `claude` process.
- NDJSON excerpts above are abbreviated and redacted (usage, tools, skills, paths outside the spike dir, account data removed). The original raw files were removed after use; raw excerpts for steps 1, 2 and 5 were re-captured under #142 (see re-run blocks); steps 3, 4, 6 and 7 quote values read at the time, raw output not retained.
- Steps 5 (Bridge half), 6 (`UNKNOWN_OUTCOME`) and 8 are NOT_EXERCISED, not PASS.
