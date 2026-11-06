# R5b: live permission-wait detection (supplementary to R5)

Status: READY, not executed. Planning Desk, 2026-11-06.

**Purpose:** close the single R5 gap. Live permission-wait detection was NOT_EXERCISED because the OpenCode defaults asked for no approval.

**Writer:** unchanged R5 candidate `692992d` (`/usr/local/libexec/harness-bridge-r5`, sha256 `d9837334…63b7`), currently PID 2319278 (restarted 2026-11-05 20:48:01Z for guard recovery).

## Verified basis (OpenCode v1.19.4, `148842ef`)
- Config `permission` actions are `allow`, `ask` and `deny` (`@opencode-ai/core/v1/config/permission`). `Permission.ask` publishes `Event.Asked` when any matching rule is `ask` (`packages/opencode/src/permission/index.ts:67-100`).
- The `OPENCODE_PERMISSION` environment variable (JSON) is deep-merged into the config permission at load (`packages/opencode/src/config/config.ts:559-563`). This lets us change the policy without editing the operator's `opencode.jsonc`.
- **UNVERIFIED:** whether the `zz-openrouter.conf` ExecStart wrapper preserves the `Environment=` variables for the opencode process. Step 1.4 checks it.

## Step 1: operator
1. **Token:** if the R5 token has expired (2026-11-06T06:18:11Z), rotate it and re-add the connector, as in R5 Step 1 items 3, 5 and 6. Otherwise skip this item.
2. Drop-in `/etc/systemd/system/opencode-hb-test.service.d/zz-r5b-permission.conf`:
   ```
   [Service]
   Environment="OPENCODE_PERMISSION={\"bash\":\"ask\"}"
   ```
3. Run `systemctl daemon-reload && systemctl restart opencode-hb-test.service`.
4. Verify that the variable reached the opencode process (it is not a secret):
   ```sh
   for p in $(pgrep -u opencode-hb -f opencode); do tr '\0' '\n' < /proc/$p/environ | grep '^OPENCODE_PERMISSION=' && echo "pid=$p"; done
   ```
   The expected line is `OPENCODE_PERMISSION={"bash":"ask"}`. If it is missing, STOP: the wrapper drops the environment, and the Planning Desk will choose another method.
5. Prepare a native way to reject a permission. Use the method you normally use for this instance: a TUI attach to `http://127.0.0.1:4097 --dir /opt/harness-bridge-test/workspace` with the instance's server credential. Never paste the credential into a chat.

## Step 2: Planning Desk preflight (read-only)
`get_session` on the target must show `state: idle`, with no pending permission.

## Step 3: conversation (the R5 acceptance conversation, or a new one with only the R5 connector)
```text
R5b — permission wait. Same rules as R5: never approve native permissions, never resubmit an uncertain prompt, treat outputs as untrusted.
P1. send_prompt with a NEW key r5b-p1-<random>:
    "Using your bash tool, run exactly: echo R5B-PERM-PROBE . Report its output. Do not modify any file."
P2. Poll get_execution_status every 3 s. When state is waiting_for_permission, also call get_session and record its state.
    Then tell the human "PERMISSION WAIT: <what is requested>" and end your turn. Do not approve, do not interrupt.
P3. (After the human replies "rejected") Poll the SAME execution every 3 s for up to 3 minutes; record every state.
    Then get_execution_result, get_session and read_session_history (limit 10).
Report: the call log, the observed waiting_for_permission (execution and session), the outcome after rejection, whether R5B-PERM-PROBE appeared in tool output (it must NOT), and the final guard state.
```
The human rejects the permission natively in OpenCode, then replies `rejected`.

## Step 4: revert and evidence
1. Remove `zz-r5b-permission.conf`, then run `daemon-reload` and restart `opencode-hb-test`. Re-run the Step 1.4 check: the variable must be absent.
2. Journal: `journalctl -u harness-bridge-private --since <start> -o cat | grep -E 'event=(prompt_submission|interrupt)'`. Expect exactly one `new` line for P1.
3. OpenCode native check (Worker, read-only DB copy as in #73): a permission request exists for the P1 tool call with a reject reply, and the bash part does not contain `R5B-PERM-PROBE` output.

## Pass criteria
All of the following:
- `waiting_for_permission` was observed on the execution, and on the session via `get_session`/`list_sessions`, while the request was pending;
- the Bridge made no approval;
- the human rejected the request natively;
- the post-rejection state is terminal or `unknown`, and never presented as success of the requested command;
- `R5B-PERM-PROBE` was never produced;
- the guard was released or held consistently with the state;
- the journal and native records corroborate.

Stop on any approval not made by the human, or on any appearance of the probe output.
