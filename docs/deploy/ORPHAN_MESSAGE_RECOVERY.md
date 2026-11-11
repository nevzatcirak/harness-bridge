# Orphan assistant message recovery (F-6)

Status: runbook, 2026-11-06; Step 4 updated 2026-11-08 for the ADR-020 (b)
fenced guard release (release first, restart only as fallback). Finding F-6 (R5/R5b, P2). Applies to OpenCode
1.19.4 (`anomalyco/opencode` `148842ef7cb1bc34192b3fff57d0f9721ab9c801`). Source
paths below are relative to that tree's `packages/`.

**Recovery (Step 3) is a destructive, maintainer-only action on the host.** The
Bridge never performs it and has no tool for it. Claude Chat (or any MCP client)
must never be asked to perform it, and the native credential must never be pasted
into a chat, an issue, a log or a command line.

## What happens

If OpenCode restarts while an assistant turn is running a tool call, the assistant
message stays in the native database without `time.completed`, and its tool part
stays `running`. The new OpenCode process has no runner for that session:

- The runner map is in memory per instance
  (`opencode/src/session/run-state.ts:35-50`). A restarted process starts empty.
- With no runner, the session is absent from `GET /session/status`. `SessionStatus.set`
  deletes idle entries from the map (`opencode/src/session/status.ts:39-47`), so
  absence reads as idle.
- Native abort does not help. `POST /session/:sessionID/abort` calls
  `SessionPrompt.cancel` → `SessionRunState.cancel` and returns `true`
  (`opencode/src/server/routes/instance/httpapi/handlers/session.ts:232-235`,
  `opencode/src/session/prompt.ts:152-155`). With no runner, `cancel` only cancels
  background jobs and sets status `idle`. It does not complete, fail or remove the
  message (`opencode/src/session/run-state.ts:77-86`, no-runner branch `81-84`).
  The Bridge's `interrupt_execution` uses this same native abort, so it cannot
  clear an orphan either.

The orphan is left in place indefinitely, and the Bridge refuses every new prompt
for that session.

## 1. Symptoms

- `send_prompt` fails before submission with
  `SESSION_BUSY`, message `unfinished native assistant message`
  (`internal/execution/manager.go`, admission history check). Nothing is sent and
  the idempotency key is not accepted.
- Discovery may still show the session as `idle`, often with
  `native_state_inferred_from_absence: true`, because native status has no entry
  for it. This mismatch is F-5. Until F-5 is merged (PR #82), only the
  `send_prompt` refusal reveals the orphan. After F-5, `get_session` reports
  `state: "unknown"` with `native_unfinished_message: true`.
- Native shape of the orphan (an entry in `GET /session/:id/message`):
  - `info.role` is `"assistant"`;
  - `info.time.created` is set and `info.time.completed` is absent;
  - there is no `info.finish` and no `info.error`;
  - one or more tool parts have `state.status: "running"`, no output and no exit code.
- The R5 Step 4 attempt-2 record shows exactly this shape
  (`docs/evidence/R5_EVIDENCE_2026-11-05.md`, "Step 4").

## 2. Detection (read-only)

Do every step here before considering Step 3. None of them change state.

1. **Find the message.** Use either:
   - the Bridge: `read_session_history` for the session (read-only; any authorized client may do this); or
   - native, on the host:
     `GET /session/<SESSION_ID>/message?directory=<SESSION_DIRECTORY>`.
     Authenticate as in the Step 3 command shape (credential via curl's stdin
     config, never on the command line). Ready-made command, the Step 3 shape
     without `-X DELETE`. **Run in bash**; every `<…>` is a placeholder:
     ```bash
     set +o history   # keep the commands out of shell history
     set +x           # no xtrace: a traced printf line would print the password
     PWFILE=<OPENCODE_SERVER_PASSWORD_FILE>   # as configured for the Bridge; see CREDENTIAL_FILES.md
     SESSION=<SESSION_ID>                     # ses_…
     DIR=<SESSION_DIRECTORY>                  # the session's own directory
     builtin printf 'user = "%s:%s"\n' "${OPENCODE_SERVER_USERNAME:-opencode}" "$(<"$PWFILE")" \
       | curl --silent --show-error --fail-with-body --config - \
           --get --data-urlencode "directory=$DIR" \
           "http://127.0.0.1:<OPENCODE_PORT>/session/$SESSION/message" \
       > <READ_ONLY_COPY>.json
     ```
     The output is the full history; keep it as the read-only copy Step 3
     asks for. Never use `-u user:password`, which puts the password in the
     process list.

     **Passwords containing `"` or `\`.** The curl config line above wraps the
     value in double quotes, and curl's config syntax treats `\` as an escape
     inside them. A password containing a literal `"` or `\` therefore breaks the
     line. Measured with curl: an unescaped password `p"w\x` was sent as `p`, cut
     at the first quote, so the request fails authentication; it is a hard
     failure, not an injection (`readSecret` already rejects CR, LF and NUL). Escape the
     backslash first, then the quote, before `printf`, with shell builtins only
     (no external command sees the password):
     ```bash
     PW=$(<"$PWFILE")
     PW=${PW//\\/\\\\}   # \ -> \\
     PW=${PW//\"/\\\"}    # " -> \"
     builtin printf 'user = "%s:%s"\n' "${OPENCODE_SERVER_USERNAME:-opencode}" "$PW" \
       | curl --silent --show-error --fail-with-body --config - … (rest as above)
     unset PW
     ```
     The same escaping applies to the Step 3 command. A username containing `"`
     or `\` needs the same treatment.

   Select assistant messages without completion:
   ```sh
   jq -r '.[] | select(.info.role == "assistant" and ((.info.time.completed // 0) <= 0))
         | "\(.info.id) created=\(.info.time.created)"'
   ```
   Record the message ID (`msg_…`) and `time.created` (epoch milliseconds). Expect
   exactly one. If there are several, or none, stop: this runbook does not apply.
2. **Confirm that no native runner owns it.** All of the following must hold:
   - `GET /session/status?directory=<SESSION_DIRECTORY>` has no entry for the
     session, or an `idle` entry. `busy` or `retry` means work is active: stop.
   - The OpenCode service started **after** the message was created. Compare
     `systemctl show -p ActiveEnterTimestamp <OPENCODE_UNIT>` and the restart
     lines in `journalctl -u <OPENCODE_UNIT> --since <time before the message>`
     with `time.created`. A runner exists only in the process that started the
     turn. If the current process started before the message was created, it
     may still own the turn: stop.
   - No process from before that restart survives in the unit's cgroup. Run
     `systemctl status <OPENCODE_UNIT>` and check the processes in its CGroup
     tree, with `ps -o pid,lstart,args -p <PID>` for each one. Every one must have
     started after the latest OpenCode restart. A tool child process of the
     interrupted turn (for example a long `sleep` or a build) can survive a
     restart when the unit's `KillMode` is not `control-group`, and it can keep
     writing files. If one exists: stop.
   - The OpenCode journal since that start shows no activity for the session,
     and the native client UI (if used) shows nothing running.
   - No `GET /permission?directory=<SESSION_DIRECTORY>` entry exists for the
     session. A pending permission is a live wait, not an orphan.
3. **Confirm the Bridge is not still working on it.**
   `get_execution_status` for any execution recorded for the session should be
   terminal or `unknown`. A Bridge `unknown` here is expected (see Step 4). Do not
   resubmit anything.
4. **Wait and re-read.** Read the message again after at least one minute. It
   must be unchanged: still no `time.completed`, same parts.

If any check fails or is unclear, stop and record what you saw. Do not delete.

## 3. Recovery: delete the orphan message (destructive, maintainer only)

Native route: `DELETE /session/:sessionID/message/:messageID`, identifier
`session.deleteMessage`
(`opencode/src/server/routes/instance/httpapi/groups/session.ts:102`, `409-421`).
What the source does:

- The handler checks the session exists. It then refuses with HTTP 409
  `SessionBusyError` when **this instance** holds a busy runner for the session.
  Otherwise it publishes `message.removed` and returns `true`
  (`handlers/session.ts:380-387`, `handlers/session-errors.ts:10-21`,
  `opencode/src/server/routes/instance/httpapi/errors.ts:116-123`,
  `run-state.ts:71-75`).
  - The busy check only sees the in-memory runner of the process serving the
    request. It does **not** protect a turn owned by another process, and it is
    not a substitute for Step 2.
- The `message.removed` projection deletes the message row, adjusting session
  usage. Its parts are removed by `ON DELETE CASCADE`
  (`core/src/session/projector.ts:274-292`, `core/src/session/sql.ts:82-90`).
  SQLite enforces the cascade only with foreign keys enabled; OpenCode enables
  them when it opens its database (`core/src/database/database.ts:31`,
  `PRAGMA foreign_keys = ON`).
- The route description says: "Permanently delete a specific message and all of
  its parts from a session without reverting file changes"
  (`groups/session.ts:418-419`).
  - Files the interrupted tool already wrote stay on disk.
  - There is no undo. Take a read-only copy of the session history (Step 2.1
    output) before deleting.

Delete **only** the one assistant message ID confirmed in Step 2. Never delete
the user message that preceded it, and never delete in bulk.

**Immediately before the DELETE**, re-check, because Step 2 may be minutes old
and the 409 check only sees this instance's own runner:
1. Repeat the Step 2.2 reads: `GET /session/status?directory=<SESSION_DIRECTORY>`
   (no entry, or `idle`) and `GET /permission?directory=<SESSION_DIRECTORY>` (no
   entry for the session).
2. Confirm that no other native client (an OpenCode TUI or the web UI) is
   attached to the session and that no one will start a turn in it while you
   delete. Close or detach any that are.
3. If anything differs from Step 2, stop and start again at Step 2.

Command shape, run by the maintainer on the host. **Run in bash.** Every `<…>`
is a placeholder:

```bash
set +o history   # keep the commands out of shell history
set +x           # no xtrace: a traced printf line would print the password
PWFILE=<OPENCODE_SERVER_PASSWORD_FILE>   # as configured for the Bridge; see CREDENTIAL_FILES.md
SESSION=<SESSION_ID>                     # ses_…
MESSAGE=<ORPHAN_MESSAGE_ID>              # msg_… from Step 2
DIR=<SESSION_DIRECTORY>                  # the session's own directory
# The bash builtin printf and $(<file) run in the shell itself, so the password
# never appears in any process's argv (/proc/*/cmdline), and with xtrace off it
# is not echoed to stderr. curl reads it from its stdin config.
# (A password containing " or \ must be escaped for curl's config syntax: see
# the escaping snippet in Step 2.1.)
builtin printf 'user = "%s:%s"\n' "${OPENCODE_SERVER_USERNAME:-opencode}" "$(<"$PWFILE")" \
  | curl --silent --show-error --fail-with-body --config - \
      --get --data-urlencode "directory=$DIR" \
      -X DELETE "http://127.0.0.1:<OPENCODE_PORT>/session/$SESSION/message/$MESSAGE"
echo
```

The Basic Auth username defaults to `opencode` when `OPENCODE_SERVER_USERNAME` is
unset (`opencode/src/server/auth.ts:18-19`). Expected output is `true`. A `409`
means a runner is active in this instance: stop, because Step 2 was wrong. `404`
means the session was not found under that routing: stop and re-check
`<SESSION_DIRECTORY>`. Never print `$PWFILE` contents, and never echo the
configuration line.

Workspace-bound sessions (non-empty `workspaceID`) are routed by workspace, not
by directory (`docs/CONTRACTS.md`, "Discovery bootstraps native instances").
This runbook does not cover them: stop and escalate.

## 4. After recovery

1. Re-read the history (read-only). The orphan ID must be gone, the preceding
   messages unchanged and in order. Count from IDs, never estimate.
2. `get_session` for the session:
   - `idle` (with F-5: no `native_unfinished_message`) means native evidence is
     clear;
   - `unknown` can still be correct. If the Bridge sent the interrupted prompt,
     its execution is `unknown`, and the Bridge's single-writer guard stays held
     (ADR-010; ADR-017 option (a) reports this but does not release it). The R5
     run recorded exactly this (`R5_EVIDENCE_2026-11-05.md`, "Guard recovery").
3. If the guard is held, **first** use the fenced guard release (ADR-020 option
   (b), accepted 2026-11-08; `docs/CONTRACTS.md`, "ADR-020 operator guard
   release"). It needs the writer started with `BRIDGE_ADMIN_SOCKET`. As the
   maintainer on the host, with a ticket ID for this recovery:
   ```bash
   sudo -u <SERVICE_USER> env BRIDGE_ADMIN_SOCKET=<ADMIN_SOCKET> \
     <BRIDGE_BINARY> admin release-guard \
       --session opencode:<SESSION_ID> --execution <EXECUTION_ID> --ticket <TICKET_ID>
   ```
   - `<EXECUTION_ID>` is the `unknown` execution that holds the guard
     (`get_session` / `get_execution_status`).
   - The Bridge re-checks everything itself, twice, 5 s apart: the guard holder,
     `unknown` with the monitor ended, identity and workspace, native status
     absent or `idle`, no unfinished assistant (so Step 3 must be done first),
     no pending permission, and the correlated user message. Expect exit `0`
     and `"outcome": "released"`. Record the printed JSON.
   - Add `--allow-missing-user-message` **only** when the Bridge's user message
     is absent natively and you accept that a late delivery could still
     arrive. The override is audited.
   - On `refused`, read `error.code` (`SESSION_BUSY`: native work, an orphan or
     a permission is still there, so go back to Step 2; `INVALID_INPUT`: wrong
     IDs, the run is no longer `unknown`, or another execution holds the guard;
     `UPSTREAM_TIMEOUT` / `UPSTREAM_FAILURE`: retry later). Do not work around
     it.
   - Then check: `get_session` is no longer guarded, the execution is still
     `unknown`, the journal has exactly one `event=guard_release
     outcome=released` line, and the Bridge PID and `NRestarts` are unchanged.

   **Fallback only** (no admin socket configured, or the release keeps being
   refused while you are sure native work has stopped): the maintainer restarts
   the Bridge in an approved window (ADR-010 procedure: fence writers, restart,
   verify the binary hash, PID and `NRestarts`). Then read `get_session` again.
4. **Keys.** After a fenced release, every idempotency record is kept: the old
   key replays the same `unknown` execution and never submits again, and a new
   prompt needs a fresh key. **Never reuse an idempotency key across a
   restart.** Idempotency records are in memory and are lost on restart, so a
   reused key would submit a new prompt, not replay the old one. Every later
   `send_prompt` uses a fresh key.
5. Record what you did:
   - session ID, message ID and `time.created`;
   - the Step 2 evidence (status, service start time, journal window);
   - the DELETE response;
   - the post-recovery `get_session`;
   - the release JSON and ticket ID, or any Bridge restart and why the release
     was not used.

   Include no credential and no prompt text.

## Limitations

- Detection proves only what the native reads and the service timeline show. A
  second OpenCode process serving the same database is outside this procedure.
- The deletion removes conversation context. The next turn cannot see the
  interrupted tool call's partial output.
- The R5b recovery result is the Planning Desk's report (issue #79). It is not
  recorded under `docs/evidence/` at the time of writing.
