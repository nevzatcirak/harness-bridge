# H5: promote a frozen main build, H5 regression, R7 lifecycle and the dashboard live acceptance

Status: **DRAFT for the H5 sitting, not executed.** Worker B, 2026-11-20, rewritten for planner task
#435 from the #322 draft (after #428/#429 and PR #427 merged). Base inspected: `main`
`8406b18fa965e839da4a025276be0b846123124b` (the merge of #427), 2026-11-20. The freeze SHA is a
placeholder: it is whatever merge the Planning Desk freezes for H5. Re-check every row and every
citation against the freeze SHA before the operator starts. Every live behaviour that has not been
observed is marked **UNVERIFIED**; a stage that is not run is **NOT_RUN**, never PASS.

**Purpose:** replace the live H4 writer with a reproducible build of frozen `main`, prove the H4
behaviour on it, then run the three live acceptances that are still open in the same sitting: H5
stage 1 (regression), R7 (ADR-022/ADR-025 lifecycle and overlay), and R-DASH (the read-only
dashboard). The Claude Code adapter stays **off** (stage 6) because its live acceptance (R6) has not
passed.

This page is the sitting script. It does not replace the per-feature reference pages; where they
disagree, the code and this page win for the sitting, and the difference is recorded in the evidence
file.

## What H5 adds over H4 (`9db1c1d`)

| Feature | On the inspected main | Switch and default |
|---|---|---|
| ADR-021 Claude Code adapter | code and wiring merged; R6 live acceptance **not passed** (run 5 pending) | `BRIDGE_CLAUDE_COMMAND` unset = off (`cmd/bridge/claude.go:34-47`); a partial set refuses startup |
| ADR-022 lifecycle S1–S5 + ADR-025 S6/S8, production wiring (D22) | **wired** by `cmd/bridge/lifecycle.go`; inert until configured | `BRIDGE_LIFECYCLE_CONFIG` unset = no lifecycle tool; `BRIDGE_OVERLAY_DIR` adds the archive tools; invalid value refuses startup (`cmd/bridge/lifecycle.go:20-28,64-120`) |
| ADR-023 dashboard | code; R-DASH not exercised | `BRIDGE_DASHBOARD_SOCKET` or `BRIDGE_DASHBOARD_ADDR`, unset = off (`internal/dashboard/config.go:24-29`) |
| ADR-024 A1 S7 incarnation evidence | code (merged after #415); **not exercised live** | `BRIDGE_PERMISSION_INCARNATION` unset/`off` = no tracker; `observed` starts it (`cmd/bridge/relay.go:35-44`) |
| ADR-024 `reply_permission` reject-only | production `PermissionSource` installed | `BRIDGE_PERMISSION_RELAY` unset/`reject_only` = registered (12 tools), `off` = opt-out (11), anything else refuses startup (`cmd/bridge/relay.go:17-28`) |
| ADR-020 (b) admin socket, ADR-019 token lifetime | as H4 | `BRIDGE_ADMIN_SOCKET`, `BRIDGE_TOKEN_MAX_LIFETIME` (see the table) |

Tool surface: 11 base tools; `+1` with `reply_permission` (the `reject_only` default); `+2` with a
valid lifecycle configuration (`list_catalog`, `create_session`); `+3` more with a valid overlay
directory held by the writer (`archive_session`, `unarchive_session`, `close_session`)
(`internal/mcp/server.go:331,336,341-343,346`). H5 stage 1 uses `BRIDGE_PERMISSION_RELAY=off` (D21
Q7), so it expects **11** tools; R7 with lifecycle `+` overlay expects **16**.

## Environment table (every variable the sitting uses)

Verified against the freeze code; line numbers are from the inspected main (`8406b18`) and must be
re-checked against the freeze SHA.

| Variable | Rule and default | Source |
|---|---|---|
| `OPENCODE_URL` | upstream base URL; default `http://127.0.0.1:4096` | `cmd/bridge/main.go:28-31` |
| `OPENCODE_SERVER_USERNAME`, `OPENCODE_SERVER_PASSWORD` (`_FILE`) | native client credentials; `_FILE` is a private regular file, `_FILE` and the direct value are mutually exclusive | `cmd/bridge/main.go:32-36`; `cmd/bridge/secret.go:13-43` |
| `BRIDGE_LISTEN` | listener; default `127.0.0.1:8080`; loopback unless remote TLS mode | `cmd/bridge/ingress.go:31-33`; `internal/security/listen.go` |
| `BRIDGE_READ_ONLY` | `0`/`1`; empty = `0`; the writer needs `0` | `cmd/bridge/ingress.go:24-30` |
| `BRIDGE_TOKEN`, `BRIDGE_TOKEN_FILE` | exactly one; file: absolute, private regular file (mode `&0077 == 0` unless a systemd credential), 1–16384 bytes | `cmd/bridge/startup.go:11-36`; `cmd/bridge/secret.go:13-43` |
| `BRIDGE_TOKEN_MAX_LIFETIME` | Go duration, default `24h`, ceiling `720h`; above `24h` the token must be ≥43 characters | `cmd/bridge/ingress.go:35-37`; `internal/security/remote.go:26-33,39-66` |
| `BRIDGE_TOKEN_EXPIRES` | RFC3339; remote mode only; must lie within `BRIDGE_TOKEN_MAX_LIFETIME` | `cmd/bridge/ingress.go:38-46` |
| `BRIDGE_PUBLIC_ORIGIN`, `BRIDGE_PRINCIPAL` | remote-mode origin and principal; any remote setting requires TLS | `cmd/bridge/ingress.go:38-50` |
| `BRIDGE_TLS_CERT`, `BRIDGE_TLS_KEY` | remote mode only; both required, never a plaintext fallback | `cmd/bridge/ingress.go:23,38-41` |
| `BRIDGE_ALLOWED_ROOTS` | path-list (`:`); the static ACL roots | `cmd/bridge/main.go:40`; `internal/security` |
| `BRIDGE_ALLOWED_SESSIONS` | comma list of exact session IDs; **static**, read once at startup | `cmd/bridge/main.go:40` |
| `BRIDGE_WRITER_LOCK_DIR` | the shared writer-lock directory (also used by the Claude adapter) | `cmd/bridge/main.go:51`; `cmd/bridge/claude.go:50` |
| `BRIDGE_ADMIN_SOCKET` | writer-only admin socket; parent `0700`, service user; ignored under read-only | `cmd/bridge/admin.go:18-31` |
| `BRIDGE_LIFECYCLE_CONFIG` | absolute path of the lifecycle JSON; unset = no lifecycle tool; invalid = startup error | `cmd/bridge/lifecycle.go:20,64-78`; `internal/lifecycle/config.go:24,141-148` |
| `BRIDGE_OVERLAY_DIR` | overlay directory; needs `BRIDGE_LIFECYCLE_CONFIG`; absolute, real directory (no symlink in the final component), owned by the service user, mode exactly `0700`; set without lifecycle = startup error | `cmd/bridge/lifecycle.go:21,30,69-101`; `internal/overlay/overlay.go:163-233` |
| `BRIDGE_PERMISSION_RELAY` | unset/`reject_only` = `reply_permission` registered; `off` = 11 tools; `policy`/other = startup error | `cmd/bridge/relay.go:17-28` |
| `BRIDGE_PERMISSION_INCARNATION` | unset/`off` = no incarnation source (every request `not_verified`); `observed` = Bridge-observed epoch (S7, not exercised) | `cmd/bridge/relay.go:35-44,50-60` |
| `BRIDGE_DASHBOARD_SOCKET` | unix socket path in a `0700` directory (recommended); never both dashboard variables | `internal/dashboard/config.go:24-29`; `docs/deploy/DASHBOARD.md` |
| `BRIDGE_DASHBOARD_ADDR` | `host:port`; host exactly `127.0.0.1` or `::1`; loopback only | `internal/dashboard/config.go:31-68` |
| `BRIDGE_DASHBOARD_REDACT_EXTRA` | JSON array of ≤16 RE2 patterns, each 1–256 bytes; additive to the shipped list; invalid = startup error | `internal/dashboard/redact.go:89-108`; `cmd/bridge/dashboard.go:18-27` |
| `BRIDGE_CLAUDE_COMMAND` | absolute root-owned wrapper; **unset in H5** (stage 6) | `cmd/bridge/claude.go:16,34-47` |
| `BRIDGE_CLAUDE_SESSIONS` | allowlist JSON, mode `0600`, absolute | `cmd/bridge/claude.go:17,49-56` |
| `BRIDGE_CLAUDE_VERSIONS` | exact `claude_code_version` list, comma separated, no default | `cmd/bridge/claude.go:18` |
| `BRIDGE_CLAUDE_HISTORY_DIR` | private absolute directory of history records | `cmd/bridge/claude.go:19` |
| `BRIDGE_CLAUDE_HOME` | the Claude service user's HOME (absolute) | `cmd/bridge/claude.go:20` |
| `BRIDGE_CLAUDE_MODEL` | model name | `cmd/bridge/claude.go:21` |
| `BRIDGE_CLAUDE_LANG` | optional; default `C.UTF-8` | `cmd/bridge/claude.go:22`; `internal/claudewire/harness.go:28,73` |
| `BRIDGE_CLAUDE_MAX_TURNS` | optional; 1–1000, default 20 | `cmd/bridge/claude.go:23,52,65-71` |
| `BRIDGE_CLAUDE_MAX_BUDGET_USD` | optional decimal | `cmd/bridge/claude.go:24` |
| `BRIDGE_CLAUDE_RUN_MAX` | optional Go duration, 1s–24h, default 15m | `cmd/bridge/claude.go:25`; `internal/adapters/claudecode/runner/executor.go:101-102` |

## Step 0: build (two workers, no host changes)

Freeze SHA: `<FREEZE_SHA>` (Planning Desk). Build exactly as H4 (`docs/evidence/H4_RUNBOOK.md`
"Step 0"):

```sh
git clone https://github.com/nevzatcirak/harness-bridge.git h5-build
cd h5-build && git checkout <FREEZE_SHA> && git status --porcelain   # must be empty
CGO_ENABLED=0 go build -trimpath -o out/harness-bridge-h5 ./cmd/bridge
sha256sum out/harness-bridge-h5
go version -m out/harness-bridge-h5 | grep -E 'vcs\.(revision|modified)'
```

Two independent builders (H4-BUILD/H4-REPRO pattern): the sha256 values must match byte-for-byte and
`vcs.modified=false` in both. A mismatch is STOP. Record the toolchain, the freeze SHA, both sha256
values and both `go version -m` blocks in `H5_EVIDENCE_<date>.md`.

## Step 1: install, token rotation, first restart (operator, one sitting)

1. **Install the binary** (same pattern as H4 `docs/evidence/H4_RUNBOOK.md` "Step 1.1"):

   ```sh
   SRC=/opt/developer/hb-work/<builder>/h5-build/out/harness-bridge-h5
   DST=/usr/local/libexec/harness-bridge-h5
   EXPECTED=<sha256 from Step 0, confirmed by the second build>
   echo "$EXPECTED  $SRC" | sha256sum -c -
   test ! -e "$DST" || { echo "refusing: $DST exists"; exit 1; }
   install -o root -g root -m 0755 "$SRC" "$DST"
   echo "$EXPECTED  $DST" | sha256sum -c -
   go version -m "$DST" | grep -E 'vcs\.(revision|modified)'
   ```

2. **Drop-in** `/etc/systemd/system/harness-bridge-private.service.d/zzzz-h5.conf`. It must sort
   **after** `zzz-h4.conf` (systemd reads drop-ins in lexical order; `zzzz-h5` > `zzz-h4`), so it
   wins; keep `zzz-h4.conf` for rollback. Stage 1 has **no new feature switch**:

   ```ini
   [Service]
   ExecStart=
   ExecStart=/usr/local/libexec/harness-bridge-h5
   Environment=BRIDGE_READ_ONLY=0
   Environment=BRIDGE_TOKEN_MAX_LIFETIME=168h
   Environment=BRIDGE_TOKEN_FILE=/etc/harness-bridge/h5-token
   Environment=BRIDGE_TOKEN_EXPIRES=<RFC3339 expiry, at most +7 days>
   Environment=BRIDGE_PERMISSION_RELAY=off
   RuntimeDirectory=harness-bridge-admin
   RuntimeDirectoryMode=0700
   Environment=BRIDGE_ADMIN_SOCKET=/run/harness-bridge-admin/admin.sock
   ```

   The H4 drop-in keeps the OpenCode/lock/ACL values; this one swaps the binary and adds the
   stage-1 settings (token file/expiry and the relay off). `BRIDGE_READ_ONLY` must be `0` or `1` (`cmd/bridge/ingress.go:24-30`); the
   writer needs `0`. `BRIDGE_PERMISSION_RELAY=off` is D21 Q7 until ADR-024 A1 S7 is exercised live
   (`cmd/bridge/relay.go:17-28`). The admin socket is writer-only; its parent must be `0700` and
   owned by the service user (`cmd/bridge/admin.go:18-31`; ADR-020).

3. **Token rotation inside the sitting (ADR-019).** The live token expires
   **2026-11-23T17:54:48Z**; rotate it now rather than between stages:

   ```sh
   set +o history
   umask 077
   install -d -m 0700 /etc/harness-bridge && chown <bridge-uid> /etc/harness-bridge
   openssl rand -base64 48 | tr '+/' '-_' | tr -d '=' | head -c 64 \
     > /etc/harness-bridge/h5-token          # 64 chars, no stdout, no shell history
   chown <bridge-uid> /etc/harness-bridge/h5-token && chmod 0600 /etc/harness-bridge/h5-token
   date -u -d '+7 days' +%Y-%m-%dT%H:%M:%SZ  # the RFC3339 expiry for BRIDGE_TOKEN_EXPIRES
   ```

   - The token lives only in `/etc/harness-bridge/h5-token` (mode `0600`, owned by the Bridge UID);
     `zzzz-h5.conf` carries its **path** (`BRIDGE_TOKEN_FILE`) and the expiry
     (`BRIDGE_TOKEN_EXPIRES`), never the value, so the drop-in may stay `0644`. `BRIDGE_TOKEN` and
     `BRIDGE_TOKEN_FILE` are mutually exclusive (`cmd/bridge/secret.go:13-43`).
   - Keep `BRIDGE_TOKEN_MAX_LIFETIME=168h`; the explicit `BRIDGE_TOKEN_EXPIRES` must be RFC3339 and no
     more than **+7 days** (168h) from the sitting (`cmd/bridge/ingress.go:35-46`;
     `internal/security/remote.go:39-66`). The ≥43-character rule applies above 24h; the generated
     value has 64.
   - `set +o history` before typing it; never paste it into chat, a screenshot, the PR or the
     evidence file, and never let it reach stdout. Update the connector's request header in the same
     step.

4. **Fence before restart (ADR-010):** the target session is `idle`, no pending permission, no
   running child. The restart drops idempotency records, so old keys are never reused.

5. `systemctl daemon-reload && systemctl restart harness-bridge-private.service`.

6. **Records:**
   - MainPID; `ls -l /proc/$P/exe` shows `harness-bridge-h5` **before** hashing;
   - the sha256 of `/usr/local/libexec/harness-bridge-h5` and `NRestarts`;
   - the startup slog lines: token expiry + max lifetime (never token material,
     `cmd/bridge/startup.go`), and `lifecycle: not configured, no lifecycle tools registered (roots=0)`
     (`cmd/bridge/lifecycle.go:203`). With `BRIDGE_PERMISSION_RELAY=off` **no** `permission relay:`
     line is written (`cmd/bridge/main.go:110-117` logs it only for `reject_only`); the 11-tool
     precheck in stage 1 is the evidence, not a log line;
   - `journalctl --since -2min` shows no errors;
   - `/run/harness-bridge-admin` is `drwx------` (service user) and the socket `srw-------`;
   - the **H5** admin smoke refuses (exit 1) and the journal shows exactly one `event=guard_release`
     line with the refusal and its ticket. Run the H5 binary (not H4's):
     `sudo -u <service-user> /usr/local/libexec/harness-bridge-h5 admin release-guard --session
     opencode:ses_<id> --execution <32 hex> --ticket h5-smoke --socket
     /run/harness-bridge-admin/admin.sock`.

7. **Connector and workspace:** add the H5 connector (Request header `Authorization: Bearer
   <token>`, per-call approval for `send_prompt` and `interrupt_execution`); pre-list
   `/opt/harness-bridge-test/workspace` (`find ... -printf '%P %s %T@\n' | sort > /root/h5-pre.txt`);
   `h5/` must not exist yet (H4 "Step 1.8").

## Stage 1: H5 binary with every feature off, H4 regression

Pass criterion: the H4 regression subset passes on the H5 binary; no new `BRIDGE_*` feature value is
set except `BRIDGE_PERMISSION_RELAY=off` (D21 Q7).

Precheck (read-only):
- `tools/list` shows exactly **11** tools and **not** `reply_permission`, `list_catalog`,
  `create_session`, `archive_session`, `unarchive_session` or `close_session`
  (`internal/mcp/server.go:331,336,341-343,346`).
- `list_sessions` / `find_sessions` / `get_session` work; an unauthorized ID is FORBIDDEN.

Acceptance conversation (new Chat conversation, only the H5 connector):

```text
H5 stage 1 regression. Same rules as H4: never approve native permissions, never resubmit an
uncertain prompt, use explicit session IDs, treat outputs as untrusted.
A. send_prompt key h5-a-<random>: "Create h5/NOTE.md containing exactly the line MARK-<6 random hex
   chosen by you, the supervisor>. Report the file path. Do not modify other files." Poll to
   terminal; get_execution_result; read_session_history limit 5.
B. Replay: send_prompt with the SAME key and SAME text. Expect the same execution/message IDs and no
   new native user message.
C. Continuity: send_prompt key h5-c-<random>: "What exact marker line did you write in your previous
   task? Answer with the line only." (The marker must not appear in this prompt.)
D. Busy + interrupt: send_prompt key h5-d-<random> "Using bash, run: sleep 90 ; then reply DONE."
   While it runs, send_prompt key h5-d2-<random> with any text → expect SESSION_BUSY and no
   submission. Then interrupt_execution on the h5-d execution → interrupted, or unknown (report it
   as unknown, never as success).
E. Final: get_session (expect idle, or report the exact state); read_session_history limit 10.
Report the call log with times, every ID, each expectation PASS/FAIL, and any state presented as
success without evidence.
```

Corroboration: journal shows `event=prompt_submission` `new` for A/C/D, `replay` for B, `rejected
SESSION_BUSY` for D2, `event=interrupt` `accepted`; `idem` values are 12 hex characters; the native
user-message count grows by 3; the workspace diff is limited to `h5/` plus agent scratch inside it
(H4 `docs/evidence/H4_RUNBOOK.md` "Step 4"). Any token material in logs is STOP.

Rollback: remove `zzzz-h5.conf`, `daemon-reload`, restart; `zzz-h4.conf` runs the H4 binary again.
Keep the token valid for H4 (H4 supports the 168h maximum; a further rollback to R5 `692992d` needs
a ≤24h token).

## Stage 2: dashboard (DASH; R-DASH)

Switch: **either** `BRIDGE_DASHBOARD_SOCKET` **or** `BRIDGE_DASHBOARD_ADDR`, never both; unset = no
listener (`internal/dashboard/config.go:24-29`). The address must be exactly `127.0.0.1` or `::1`
(`config.go:31-68`). The unix socket is recommended (D17 Q1).

Setup:
1. Let systemd create the socket directory: add `RuntimeDirectory=harness-bridge-dashboard` and
   `RuntimeDirectoryMode=0700` to the drop-in (owned by the unit's `User=`, exactly `0700`, removed
   on stop) instead of hand-making a `/run` directory; this matches the admin socket's pattern and
   removes the unit-sandbox question.
2. Add `Environment=BRIDGE_DASHBOARD_SOCKET=/run/harness-bridge-dashboard/dash.sock` to the drop-in.
3. Optional operator redaction: `BRIDGE_DASHBOARD_REDACT_EXTRA` is implemented and wired
   (`internal/dashboard/redact.go:89-108`; `cmd/bridge/dashboard.go:18-27`); the name is still
   marked provisional in `DASHBOARD.md`/BACKLOG. JSON array of ≤16 RE2 patterns, 1–256 bytes;
   invalid = startup error, never logged. It is a convenience, not a guarantee.
4. Restart; `ls -ld /run/harness-bridge-dashboard` is `drwx------` (service user) and the socket
   `srw-------`.

Dashboard script (from the workstation, loopback only):
```sh
ssh -N -L 127.0.0.1:8765:/run/harness-bridge-dashboard/dash.sock <operator>@<bridge-host>
```
then open `http://127.0.0.1:8765/` and check:
1. **Sessions view.** The list shows exactly the sessions in `BRIDGE_ALLOWED_SESSIONS` the Bridge
   can read (the same set as `list_sessions`); no session outside the ACL. The H4 target shows its
   state, guard, current execution and last activity.
2. **Path / lifecycle root.** Open `/session/opencode/<id>`: the detail shows the session's root
   (path), state, guard, current execution, and the lifecycle-root label (`-` until stage 3, then
   the configured root) (`internal/dashboard/views.go:112,192,208`).
3. **Diff view.** With the stage-1 `h5/NOTE.md` present, the working-tree diff (`/vcs/diff`, no
   `git` run by the Bridge) shows that file and nothing outside the workspace; a per-turn diff
   appears for the A turn, or the page says it is not computed.
4. **Redaction check.** Put an obviously **fake** pattern in a file under `h5/` (for example
   `sk-ant-FAKE0000000000000000`), reload the diff: it renders `[redacted]`, and the page's
   redaction count is ≥1 (`internal/dashboard/redact.go:9,69-76,78`). Delete the fake line afterwards.
5. **Archived filter.** After the R7 archive step, `/` hides the archived session and
   `/?archived=1` shows it with the `archived` badge (`internal/dashboard/handler.go:169-172`).
6. **Audit.** `/audit` shows the ring: `guard_release`, `prompt_submission`, `interrupt`,
   `session_create` only (`internal/dashboard/audit.go:32`); the archive events stay journal-only
   until the allowlist is amended. No free text, no token material.
7. **Read-only.** On the **unix socket** `curl -i -X POST http://127.0.0.1:8765/api/state` is 405
   (every method but GET/HEAD) and a request carrying **any** `Origin` header is 403
   (`internal/dashboard/handler.go:123-139,179-182`; the Host allowlist is empty on the socket,
   `internal/dashboard/server.go:55-58,71-74`). The foreign-`Host` 400 path is **NOT_EXERCISED on the
   socket**: it exists only with a temporary loopback `BRIDGE_DASHBOARD_ADDR` run, which this sitting
   does not add. A failing source is 503 with `Retry-After`. There is no write route.

Pass criterion: the list equals the ACL set, the detail shows path/state/lifecycle root, the diff is
the session's working tree only, the fake pattern is `[redacted]`, the archived filter works after
R7, `/audit` has no free text or token material, on the socket every non-GET/HEAD is 405 and any
`Origin` is 403 (the foreign-`Host` 400 is NOT_EXERCISED, never PASS). A page that renders a
session outside the ACL is FAIL. Everything not observed is NOT_EXERCISED, never PASS.

Rollback: remove `BRIDGE_DASHBOARD_SOCKET` (and any `BRIDGE_DASHBOARD_ADDR`), restart; the socket
directory may stay.

## R7: lifecycle create/catalog and overlay archive (stages 3+4)

Prerequisite: freeze SHA contains the D22 wiring (the inspected main does). R7 runs after stage 1
and stage 2 in the same sitting. `BRIDGE_PERMISSION_RELAY=off` stays. The test root is
`/opt/harness-bridge-test/workspace`, and creation needs an existing directory whose Git top level
lies inside it (the Bridge never creates directories; a directory without a Git top level is
`FORBIDDEN non_git`, `internal/lifecycle/authz.go:26,135-221`).

**Prepare (operator, before the restart):**
```sh
ROOT=/opt/harness-bridge-test/workspace
stat -c '%U %G %a' "$ROOT"                            # read the workspace's owner, group and mode
R7DIR="$ROOT/r7-$(head -c4 /dev/urandom | xxd -p)"    # max_depth 1: one level below the root
# Create it with the SAME owner, group and mode as the workspace (the agent must be able to write
# in it), then make it a Git top level as that owner so the create target is not refused as non_git:
install -d -o <owner> -g <group> -m <mode> "$R7DIR"
sudo -u <owner> git -C "$R7DIR" init -q
# lifecycle config: absolute canonical path, no symlink in any component, mode 0600, owned by the
# Bridge process's UID (the unit's User=; root when User= is empty):
cat > /etc/harness-bridge/lifecycle-h5.json <<'JSON'
{"roots":[{"path":"/opt/harness-bridge-test/workspace","modes":["create","archive"],
           "max_sessions":20,"max_depth":1,"existing_tools":false}],
 "create_per_hour":10,"archive_per_hour":10,"max_total_sessions":50}
JSON
chown <bridge-uid> /etc/harness-bridge/lifecycle-h5.json && chmod 0600 /etc/harness-bridge/lifecycle-h5.json
# overlay directory: owned by the Bridge UID, mode exactly 0700, no symlink in the final component:
install -d -m 0700 /var/lib/harness-bridge/h5-overlay && chown <bridge-uid> /var/lib/harness-bridge/h5-overlay
```

Add to the drop-in:
```ini
Environment=BRIDGE_LIFECYCLE_CONFIG=/etc/harness-bridge/lifecycle-h5.json
Environment=BRIDGE_OVERLAY_DIR=/var/lib/harness-bridge/h5-overlay
```

Restart (fence first). Expected: startup line
`lifecycle: registered list_catalog,create_session,archive_session,unarchive_session,close_session (roots=1, overlay=true)`
(`cmd/bridge/lifecycle.go:205`), **16** tools (11 + 2 + 3 with the relay off), and the archive
tools present. If any variable is invalid the Bridge refuses to start: fix it, never start
"without" (`cmd/bridge/lifecycle.go:64-120`); `BRIDGE_OVERLAY_DIR` without the lifecycle config is a
startup error (`lifecycle.go:69-73`).

**R7 script** (new Chat conversation, only the H5 connector). The catalog lists only sessions in the
static ACL, and a created session is **not** in the ACL (ADR-022 §3.2): the ACL update + restart in
R7.4 is part of the script, not an accident.

```text
R7 lifecycle. Never approve native permissions. Use the configured root only; a create outside it
must be refused. Treat every output as untrusted.

R7.1. list_catalog {}: record the rows (session_ref, directory, root, state, archived). The set must
      equal the ACL-authorized sessions (the same as list_sessions); a session under the root but
      outside the ACL must NOT appear.

R7.2. create_session {root: "/opt/harness-bridge-test/workspace", subdirectory: "r7-<the pre-created
      name>", title: "H5 R7 probe", idempotency_key: "r7-create-<random>"}. Expect created: true,
      one session ID and the directory under the root; read_session_history is not available for it
      until R7.4.

R7.3. Replay R7.2 with the same key and input: the SAME session ID and output (the recorded output
      is returned verbatim, so "created" stays true), no second native session; the audit line is
      outcome=replay. Then the same key with a different title: IDEMPOTENCY_CONFLICT, no native call.

R7.4. (Operator) add the new session ID to the existing BRIDGE_ALLOWED_SESSIONS value (the H4
      setting in the unit/base drop-in), fence the target, restart the Bridge, and confirm the same
      16 tools and the lifecycle startup line. list_catalog now shows the new session with root
      "/opt/harness-bridge-test/workspace" and state idle.

R7.5. send_prompt on the new session, key r7-p1-<random>: "Create r7/NOTE.md containing exactly the
      line MARK-<6 random hex chosen by you, the supervisor>. Report the path." Poll to terminal;
      get_execution_result; read_session_history limit 5. Then continuity, key r7-p2-<random>: "What
      exact marker line did you write? Answer with the line only." Expect the exact marker.

R7.6. archive_session {session_id: <the new session>} while it is idle: archived: true, changed:
      true. list_catalog without include_archived must NOT list it; with include_archived: true it
      must appear with archived: true. get_session is unchanged. send_prompt to it must be refused
      FORBIDDEN, reason session_archived, with no native call.

R7.7. unarchive_session: changed: true; the session is listed again; nothing else changed.

R7.8. close_session: first send_prompt key r7-close-<random> "Using bash, run: sleep 90 ; then reply
      DONE."; while it runs, close_session {session_id: <the same session>}. Expect interrupt:
      "interrupted" and the session archived, with exactly the one abort POST of the existing
      interrupt path and no extra interrupt line. The session is hidden from list_catalog.

Report every tool result, each expected audit line, the native request log, the workspace diff
(only `r7-<hex>/`: its `.git/` and `r7/NOTE.md`), and any state presented as success without evidence.
```

**Expected audit lines** (one line per call, free-text-free; `internal/mcp/audit.go:211,272`):
- `event=session_create tool=create_session outcome=created|replay|rejected accepted=... root_id=0
  session_id=... key_hash=<16 hex> limit_state=hour:.../10,root:.../20,total:.../50 principal=...`
  (`created` for R7.2, `replay` for R7.3 first call, `rejected` with `error_code=IDEMPOTENCY_CONFLICT`
  for R7.3 second call).
- `event=session_archive tool=archive_session outcome=archived ... overlay_generation=<n>
  overlay_entries=<n> root_id=0 session_id=...` (R7.6); a replay of the same archive is
  `outcome=already_archived`.
- `event=session_unarchive tool=unarchive_session outcome=unarchived ...` (R7.7).
- `event=session_close tool=close_session outcome=closed ... interrupt=interrupted ...` (R7.8).
- `event=prompt_submission outcome=new` for R7.5 and the R7.8 prompt. The close's interrupt writes
  **no** tool-layer `event=interrupt` line: `close_session` calls the manager's interrupt below the
  tool layer (`internal/mcp/archive.go:327-330`), so its only line is the `session_close` line with
  `interrupt=interrupted`.

Pass criterion: R7.1–R7.8 as stated; `list_catalog` equals the ACL set; the replay creates nothing;
the archive hides the session from the catalog, the dashboard and `send_prompt` while `get_session`
stays unchanged; unarchive restores visibility only; close interrupts and archives with exactly one
abort POST and one `session_close` line; no native write from archive/unarchive. A replay that
creates a second session, a catalog row outside the ACL, or a hidden session that still accepts a
prompt is FAIL. Live native behaviour is UNVERIFIED until run.

Rollback: remove `BRIDGE_LIFECYCLE_CONFIG` and `BRIDGE_OVERLAY_DIR`, restart; the five tools
disappear, created sessions remain native (still listed after the ACL update), and `overlay.json`
stays on disk unread — archived sessions become visible and accept prompts again
(`docs/deploy/SESSION_LIFECYCLE_RUNBOOK.md` "Enabling and rollback").

## Stage 5: permission relay — NOT_RUN in H5

**D21 Q7:** until ADR-024 A1 S7 is accepted and exercised live, the operating setting is
`BRIDGE_PERMISSION_RELAY=off` (`cmd/bridge/relay.go:17-28`), so `reply_permission` is not registered
and this stage is **NOT_RUN**; it is not a pass. Keep `BRIDGE_PERMISSION_INCARNATION` unset/`off`
(the S7 tracker is off; `relay.go:35-44,50-60`). The startup log says
`permission relay: reject_only; no native incarnation source ...` only when the relay is registered;
with `off` there is no relay line and 11 tools.

After S7 acceptance (a later sitting): unset `BRIDGE_PERMISSION_RELAY`, confirm 12 tools, the
startup line for `incarnation=observed` if it is enabled, and use
`docs/deploy/PERMISSION_RELAY_RUNBOOK.md`. The verified path (native incarnation evidence on a real
OpenCode) remains UNVERIFIED; every unverified request stays `FORBIDDEN not_verified` with no native
write.

Rollback: `BRIDGE_PERMISSION_RELAY=off`, restart; the tool disappears.

## Stage 6: Claude Code adapter — OFF in H5

**Explicitly disabled in H5.** The code is wired (`cmd/bridge/claude.go`), but ADR-021's live
acceptance R6 has not passed (run 5 `-items 4,6` is pending, `docs/deploy/CLAUDE_CODE_RUNBOOK.md`
§7a). Enabling before that acceptance is a STOP. With `BRIDGE_CLAUDE_COMMAND` unset, every other
`BRIDGE_CLAUDE_*` value must also be unset or the Bridge refuses to start (no partial enablement,
`cmd/bridge/claude.go:34-47`); a read-only ingress ignores the adapter (`claude.go:85-95`).

When it is enabled in a later sitting, the task's intended values are: wrapper
`/usr/local/libexec/hb-claude-wrapper` (root-owned; it runs the CLI as `hbclaude` with `sudo -n`,
`docs/deploy/CLAUDE_CODE_RUNBOOK.md` §1), `BRIDGE_CLAUDE_HOME` = the `hbclaude` HOME,
`BRIDGE_CLAUDE_HISTORY_DIR` = a private root-owned directory, and roots/sessions under
`/opt/harness-bridge-test/claude-ws`. All required values are in the table above; the host
prerequisites (two non-root users, the wrapper and sudoers entry, the managed settings file with
`allowManagedHooksOnly`/`allowManagedMcpServersOnly`/`allowManagedPermissionRulesOnly` and the
Read/Glob/Grep deny rules, the Claude user's `cleanupPeriodDays`) are operator work from the runbook.
`BRIDGE_CLAUDE_HISTORY_MAX_AGE` retention is not wired (#266 P3-4, BACKLOG).

Pass criterion when it is later enabled: only after the R6 live acceptance, with a `claudecode:`
session in the allowlist and ACL, send_prompt, continuity, replay, busy/interrupt, and a release of
an `unknown` execution only via `admin release-guard` after the child and its process group are
confirmed gone. Any unobserved item is NOT_EXERCISED, never PASS.

Rollback: unset **all** `BRIDGE_CLAUDE_*` values (a partial set refuses startup), restart; keep the
wrapper/sudoers/managed settings installed.

## Corroboration patterns

- Journal (same file as every AUDIT-01 line): `event=prompt_submission|interrupt|guard_release`
  (existing), `event=session_create` (R7.2/R7.3), `event=permission_reply` with
  `phase=attempt|result|refused` (stage 5 only), `event=session_archive|session_unarchive|session_close`
  (R7.6–R7.8). Free-text-free; `idem` is 12 hex; `decided_by=chat_client`, `human_verified=false` on
  permission lines (`internal/mcp/audit.go`, `internal/audit/attempt.go`).
- Dashboard: `/audit` shows only the ring allowlist (`guard_release`, `prompt_submission`,
  `interrupt`, `session_create`; `internal/dashboard/audit.go:32`); `permission_reply` and the
  archive events are journal-only until the allowlist is amended.
- Native (worker, read-only DB copy as in #73/#76): the expected user messages, the marker file, the
  workspace diff, the permission request/reply rows; never approve or replay.

## Pass criteria, evidence and UNVERIFIED

H5 passes only when all of the following are recorded:
1. Step 0: both builds byte-identical, `vcs.modified=false`, documented toolchain.
2. Step 1: the binary at `/usr/local/libexec/harness-bridge-h5`, `/proc/<pid>/exe` confirms it, the
   rotated token has ≥43 characters and an expiry ≤ +7 days, no token material in logs, the admin
   smoke refuses with exactly one `guard_release` line.
3. Stage 1: the 11-tool precheck and A–E PASS with journal corroboration.
4. Stage 2: the dashboard script PASS (sessions = ACL, path/state/lifecycle root, diff, redaction,
   archived filter, audit, 405 and 403 observable on the socket; the foreign-`Host` 400 is
   NOT_EXERCISED, never PASS).
5. R7: the 16-tool precheck and R7.1–R7.8 PASS with the expected audit lines.
6. Stage 5 is recorded **NOT_RUN** (relay off by D21 Q7); stage 6 is recorded **OFF** (R6 not
   passed). Recorded as such, never PASS.

Evidence files: **`H5_EVIDENCE_<date>.md`** (build, install, stage 1, dashboard/R-DASH) and
**`R7_EVIDENCE_<date>.md`** (lifecycle/overlay), both with times, IDs, PASS/FAIL per item, the
journal excerpts, and the workspace diff. Skips, blocked steps and unobserved behaviour are
`NOT_EXERCISED`, never PASS.

**UNVERIFIED list (as of this revision):**
- the frozen SHA itself and that it contains the D22 wiring and the #427 fix (the inspected main
  `8406b18` does);
- the real OpenCode behaviour of `POST /session` (create), the abort POST (close), the archived
  `send_prompt` refusal and the overlay under a real power loss (`docs/deploy/SESSION_LIFECYCLE_RUNBOOK.md`
  "Verification status");
- the `BRIDGE_PERMISSION_*` live path (S7) and the relay — stage 5 is NOT_RUN;
- every Claude Code behaviour — stage 6 is OFF; R6 run 5 is pending;
- the dashboard's native diff shapes and the live `?archived=1` view (fakes only);
- that the operator's run home/HOME pass the driver pre-conditions (operator step);
- all live timings, the token rotation and the connector updates (operator-present).

## Global rollback

1. Remove `zzzz-h5.conf`, `daemon-reload`, restart: `zzz-h4.conf` runs the H4 binary.
2. Remove the dashboard and lifecycle/overlay variables (they do not exist on H4); leave the files
   on disk, unread.
3. Rotate the token to the H4 constraints (H4 supports 168h; R5 `692992d` caps at 24h). The new
   token file and `BRIDGE_TOKEN_EXPIRES` live in `zzzz-h5.conf`, so removing it makes H4 start with
   its own (superseded, possibly expired) token settings: copy the new `BRIDGE_TOKEN_FILE` and
   `BRIDGE_TOKEN_EXPIRES` into `zzz-h4.conf` (H4 accepts the same 168h and ≥43 characters) or rotate
   a fresh H4 token as part of the rollback.
4. Re-add the H4 connector; remove the H5 one.
5. If stage 5 was run in a later sitting, revert the OpenCode permission drop-in (R5B Step 4.1).
6. Keep the workspace and native sessions; `h5/` and `r7-<hex>/` are the only expected new trees.

## Open questions for the Planning Desk / maintainer

1. **Freeze SHA:** which merge is frozen, and does it contain the D22 wiring and the #427 fix?
2. **R7 ACL restart:** creating a session does not widen the static ACL (ADR-022 §3.2), so R7
   needs the operator's ACL edit + restart between create and prompt. Confirm that is acceptable
   inside the sitting (it is the only way to prompt the new session today).
3. **Relay default:** `reject_only` is the registration default (D15 Q3) while the sitting rule is
   "each default OFF"; H5 uses `off` (D21 Q7), 11 tools. Confirm for the freeze SHA.
4. **`BRIDGE_OVERLAY_DIR` naming:** ADR-025 §9 Q5/Q6 (D18) name it; §4.1 and §10 still call it a
   proposal; the code reads it (`cmd/bridge/lifecycle.go:30`). Recorded in BACKLOG.
5. **Claude enablement:** R6 run 5 must pass first; the wrapper/users/host prerequisites are the
   maintainer's.
