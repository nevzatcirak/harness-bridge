# H5: promote a new main build with staged feature enablement

Status: **DRAFT, not executed.** Worker B, 2026-11-16, for planner task #321. Everything here is
an operator plan; no step has been performed. Every live behaviour that has not been observed is
marked **UNVERIFIED**, and every stage that cannot be enabled on the frozen build is marked
**NOT AVAILABLE** rather than approximated.

**Purpose:** replace the live H4 writer with a reproducible build of a *frozen* `main` that contains
everything merged since H4 (`9db1c1d`), then enable the new features one at a time, each behind its
own explicit switch, with a pass criterion and a rollback for each stage.

**Base inspected for this draft:** `main` `06728673512ba7ee028f0189c99ae89e1725cc2e`
("0672867"), 2026-11-16. The freeze SHA is a placeholder: it is whatever merge the Planning Desk
freezes for H5. The runbook must be re-checked against the freeze SHA before the operator starts;
the "not available on this base" columns below are about the code, not about this operator.

## What H5 adds, and what is actually wireable

H4 was `9db1c1d`. Since then `main` has merged:

| Feature | Merged at 0672867 | Switch and default | Wireable? |
|---|---|---|---|
| ADR-021 Claude Code adapter (default off) | yes (`1a571fc`, wiring `#264`) | `BRIDGE_CLAUDE_COMMAND` unset = off (`cmd/bridge/claude.go:16-30,34,40-47`) | config exists; **do not enable** before the ADR-021 live acceptance (stage 6) |
| ADR-022 lifecycle S1–S5 code | yes (`#246`, `#261`, `#280`) | `BRIDGE_LIFECYCLE_CONFIG` (`internal/lifecycle/config.go:24,142-148`) | **NOT AVAILABLE**: nothing in `cmd/bridge` reads it or sets `Server.Catalog`/`Server.Create`; the tools stay unregistered (`docs/BACKLOG.md:132`, `:167`) |
| ADR-023 dashboard | yes (`#270`, `#291`) | `BRIDGE_DASHBOARD_SOCKET` or `BRIDGE_DASHBOARD_ADDR`, unset = off (`internal/dashboard/config.go:27-36`) | yes (stage 2); R-DASH live still NOT_EXERCISED |
| ADR-024 `reply_permission` reject-only | yes (`#283`, `#290`) | `BRIDGE_PERMISSION_RELAY`: unset/`reject_only` = registered, `off` = opt-out, `policy`/other = startup error (`cmd/bridge/relay.go:17-32`) | registered, but **no production `PermissionSource` on 0672867** (source lands with PR #306): every call fails `UPSTREAM_FAILURE`; after #306 it fails `FORBIDDEN`/`not_verified` because no native incarnation evidence exists (stage 5) |
| Mutation Gate attempt writer | yes (`#275`), installed by the relay wiring | (no switch) `audit.Default()` (`cmd/bridge/relay.go:39-46`) | yes; it is the audit path of the relay |
| ADR-025 archive overlay S8/S6 | **no** (`#307` open at `2f94978`) | `BRIDGE_OVERLAY_DIR` is a **design proposal** (`docs/adr/ADR-025-archive-overlay-persistence.md:88`); no code references it | **NOT AVAILABLE**: not merged, and even merged it has no startup wiring (ADR-025 is still PROPOSED and has no Go code; `docs/BACKLOG.md:174-175`) |

Consequence for H5: **stages 3 (lifecycle) and 4 (overlay) cannot run on 0672867.** If the Planning
Desk freezes a build for them, the freeze SHA must contain the lifecycle startup wiring (a task that
is not yet scheduled) and, for stage 4, the ADR-025 S6 implementation plus its wiring; this runbook
gives the conditional steps and prompts so either freeze can use it. The relay's *registration*
default is `reject_only` by D15 Q3, not "off"; `BRIDGE_PERMISSION_RELAY=off` is the explicit opt-out
(see stage 5).

## Step 0: build (workers, no host changes)

Freeze SHA: `<FREEZE_SHA>` (Planning Desk; must be a merge of `main`, include the PRs named above
that the planner wants). Build exactly as H4 (`docs/evidence/H4_RUNBOOK.md:18-23`):

```sh
git clone https://github.com/nevzatcirak/harness-bridge.git h5-build
cd h5-build && git checkout <FREEZE_SHA> && git status --porcelain   # must be empty
CGO_ENABLED=0 go build -trimpath -o out/harness-bridge-h5 ./cmd/bridge
sha256sum out/harness-bridge-h5
go version -m out/harness-bridge-h5 | grep -E 'vcs\.(revision|modified)'
```

Two independent builders (H4-BUILD/H4-REPRO pattern): the sha256 values must match byte-for-byte and
`vcs.modified=false` in both. A mismatch is STOP. Record the Go toolchain (`go version`), the freeze
SHA, both sha256 values and both `go version -m` blocks in the H5 evidence file.

## Step 1: install and first restart (operator, one sitting)

1. **Install the binary** (same pattern as H4 `docs/evidence/H4_RUNBOOK.md:26-36`):

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
   RuntimeDirectory=harness-bridge-admin
   RuntimeDirectoryMode=0700
   Environment=BRIDGE_ADMIN_SOCKET=/run/harness-bridge-admin/admin.sock
   ```

   The H4 drop-in keeps the OpenCode/lock/ACL values; this one only swaps the binary and keeps the
   unchanged base configuration. `BRIDGE_READ_ONLY` must be `0` or `1`
   (`cmd/bridge/ingress.go:22-30`); the writer needs `0`. The admin socket is writer-only and is
   ignored under read-only (`cmd/bridge/admin.go:20-28`); its parent must be `0700` and owned by
   the service user (H4 step 2; ADR-020).

3. **Token (unchanged from H4 step 3, ADR-019).** `BRIDGE_TOKEN` or `BRIDGE_TOKEN_FILE`, never both
   (`cmd/bridge/secret.go:13-17`); a file must be a private regular file, 1–16384 bytes
   (`secret.go:23-43`). The maximum lifetime is `BRIDGE_TOKEN_MAX_LIFETIME`, default `24h`, ceiling
   `720h` (`internal/security/remote.go:27-29,39-58`); above 24 h the token must be at least 43
   characters (`remote.go:31-33,61-66`). For H5 keep `168h` and a ≥43-character token, expiry no
   more than +7 days. Use `set +o history`; never paste it into chat or a screenshot. The startup
   line logs the expiry and lifetime, never token material (`cmd/bridge/startup.go:5-12`).

4. **Fence before restart (ADR-010):** confirm the target session is `idle` with no pending
   permission; the restart drops idempotency records, so old keys are never reused.

5. `systemctl daemon-reload && systemctl restart harness-bridge-private.service`.

6. **Records:** MainPID; `ls -l /proc/$P/exe` shows `harness-bridge-h5` **before** hashing; sha256;
   `NRestarts`; the startup slog line; `journalctl --since -2min` has no errors; the admin socket
   directory is `drwx------` and the socket `srw-------`; the admin smoke refuses (exit 1) and the
   journal shows exactly one `event=guard_release` line with the refusal and its ticket
   (H4 `docs/evidence/H4_RUNBOOK.md:53-63`).

7. **Connector and workspace:** add the H5 connector as in H4 (Request header
   `Authorization: Bearer <token>`, per-call approval for `send_prompt` and `interrupt_execution`);
   pre-list the workspace and ensure `h5/` does not exist yet (H4 `:64-65`).

## Stage 1: binary swap with every feature off, H4 regression

Pass criterion: the H4 regression subset passes on the H5 binary; no new `BRIDGE_*` feature value is
set (the relay registration default is the only exception and it answers nothing — see stage 5).

Precheck (read-only):
- `tools/list` shows the base surface: 11 tools plus `reply_permission` = **12** (the relay is
  registered by default; D15 Q3). It must **not** show `list_catalog`, `create_session`,
  `archive_session`, `unarchive_session` or `close_session` (no lifecycle wiring).
- `list_sessions`/`find_sessions`/`get_session` work; an unauthorized ID is FORBIDDEN.

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
(H4 `docs/evidence/H4_RUNBOOK.md:85-93`). Any token material in logs is STOP.

Rollback: remove `zzzz-h5.conf`, `daemon-reload`, restart; `zzz-h4.conf` runs the H4 binary again.
Keep the token valid for H4 (H4 supports the same 168 h maximum; if rolling further back to R5
`692992d`, rotate to ≤24 h as in H4 `:106-109`).

## Stage 2: dashboard (ADR-023)

Switch: **either** `BRIDGE_DASHBOARD_SOCKET` **or** `BRIDGE_DASHBOARD_ADDR`, never both; unset = no
listener (`internal/dashboard/config.go:27-36`). The address must be exactly a loopback literal
`127.0.0.1` or `::1` (`config.go:45-68`). The unix socket is recommended (D17 Q1).

Setup:
1. `install -d -m 0700 -o <service-user> /run/harness-bridge/dashboard` (mode exactly `0700`, no
   symlink; `docs/deploy/DASHBOARD.md` "Enable it").
2. Add to the drop-in: `Environment=BRIDGE_DASHBOARD_SOCKET=/run/harness-bridge/dashboard/dash.sock`.
3. Optional operator redaction: `BRIDGE_DASHBOARD_REDACT_EXTRA` is a **slice-5a proposal name**, a
   JSON array of at most 16 RE2 patterns of 1–256 bytes (`internal/dashboard/redact.go:88-108`);
   invalid = startup error, never logged. This is a convenience, not a guarantee.
4. Restart and check `ls -ld /run/harness-bridge/dashboard` (`drwx------`, service user) and the
   socket (`srw-------`).

Precheck/acceptance (from the workstation, over SSH, D17 Q1):
```sh
ssh -N -L 127.0.0.1:8765:/run/harness-bridge/dashboard/dash.sock <operator>@<bridge-host>
```
then open `http://127.0.0.1:8765/`. The list shows exactly the ACL sessions the Bridge can read;
`/session/<harness>/<id>` opens a detail (state, guard, current execution, diffs where available);
`/audit` shows the ring; `/api/state` and `/api/state/session/...` answer JSON; any method other than
GET/HEAD is 405; a foreign Host is 400 and a foreign Origin 403 on TCP (on the unix socket any
Origin is 403); a failing source answers 503 with `Retry-After` (`internal/dashboard/handler.go`
and `docs/deploy/DASHBOARD.md`). Confirm there is no write route. **R-DASH (the operator-present
live acceptance) remains NOT_EXERCISED** until this step is actually run; the native diff shapes are
UNVERIFIED against the live server.

Rollback: remove `BRIDGE_DASHBOARD_SOCKET` (and any `BRIDGE_DASHBOARD_ADDR`), restart; the socket
directory may stay.

## Stage 3: lifecycle roots, create and catalog — **CONDITIONAL / NOT AVAILABLE on 0672867**

**On 0672867 there is no wiring:** `BRIDGE_LIFECYCLE_CONFIG` is read by
`internal/lifecycle.FromEnv` (`internal/lifecycle/config.go:142-148`) but nothing in `cmd/bridge`
calls it; `docs/BACKLOG.md:132` says the production wiring is a separate task, and the tool surface
stays at 12 (stage 1). Do not set the variable and expect a change; setting it has no effect on this
build. If the freeze SHA contains the wiring, use the steps below.

Configuration (ADR-022 §3.1, `docs/adr/ADR-022-session-lifecycle.md:103-107`; keys
`internal/lifecycle/config.go:219-232,390-391`): `BRIDGE_LIFECYCLE_CONFIG` is the path to a JSON file the
service user owns, mode `0600`, read once at startup (the Bridge never rewrites it; the path must be
canonical with no symlink in any component, and the file is swapped between inspection and open
refused). Example (all values are defaults/proposals from ADR-022 §8):

```json
{
  "roots": [
    {"path": "/opt/harness-bridge-test/h5", "modes": ["create", "archive"],
     "max_sessions": 20, "max_depth": 1, "existing_tools": false}
  ],
  "create_per_hour": 10,
  "archive_per_hour": 10,
  "max_total_sessions": 50
}
```

Defaults: `create_per_hour` 10, `archive_per_hour` 10, `max_sessions` 20 per root,
`max_total_sessions` 50 (`internal/lifecycle/config.go:33-38`); `max_depth` absent = 0, only the
root itself (`config.go:96-98`); `existing_tools` default false and grants nothing until the ADR-012
amendment (S9) (`config.go:99-102,232`). A root must exist, be canonical, be a directory owned by the
service user or root, not be `/`, and not overlap another root; non-git directories are refused
(`non_git`) because the Git top level would be outside the root (ADR-022 §3.2,
`docs/adr/ADR-022-session-lifecycle.md:130-160`). A root may be a container; the create target's Git
top level must lie inside it (run `git init` in a plain directory first; the Bridge never creates
directories).

Acceptance (conditional on the wiring; R7-style):
```text
H5 stage 3 lifecycle. Use the explicit root <root> and the configured session ACL; never create
outside it.
L1. list_catalog: record the sessions and their root labels. The set must equal the ACL-authorized
    sessions (the same as list_sessions); a session under the root but outside the ACL must NOT
    appear.
L2. create_session {root: <root>, subdirectory: "probe-<random>", title: "h5 stage 3 probe",
    idempotency_key: "h5-l2-<random>"}. Expect one new session, Created true, the directory under
    the root. Poll list_catalog until it appears with the root label.
L3. Replay L2 with the SAME key and input: same session ID, no second native creation.
L4. create_session with a different title and the same key: IDEMPOTENCY_CONFLICT.
L5. Run a prompt in the new session (send_prompt) and read its history; then get_session.
Report the IDs, the catalog rows before/after, each expectation PASS/FAIL, and any native write the
Bridge made beyond the one create.
```
Corroboration: exactly one `event=session_create` line per call with `outcome=created`/`replay`/
`rejected` and no free text (`internal/mcp/audit.go:201-209`); the native session exists under the
root; `list_catalog` equals the ACL set.

Rollback: unset `BRIDGE_LIFECYCLE_CONFIG`, restart; the tools disappear (the created sessions remain
native and are still listed by `list_sessions`).

## Stage 4: overlay archive/close — **CONDITIONAL / NOT AVAILABLE on 0672867**

**Not merged and not wired:** PR #307 (ADR-025 S6) is open; even merged, `BRIDGE_OVERLAY_DIR` is a
design proposal (`docs/adr/ADR-025-archive-overlay-persistence.md:88`) with no code reference
(“The proposed `BRIDGE_OVERLAY_DIR` … are proposals, not decisions”, `docs/BACKLOG.md:174-175`). Do not
set it on 0672867. If the freeze SHA contains the S6 merge **and** its startup wiring, use:

- Directory: **`BRIDGE_OVERLAY_DIR`** (proposal name): absolute, owned by the Bridge service user,
  mode exactly `0700`, no symlink in the final component; files `overlay.json` (mode `0600`, regular,
  single link) and `overlay.lock` (ADR-025 §4.1). Unset = the archive tools are not registered.
- The archive tools also need a lifecycle root with the `archive` mode (ADR-022 §7.3); the same
  `BRIDGE_LIFECYCLE_CONFIG` as stage 3.

Acceptance (conditional):
```text
H5 stage 4 overlay. Use only the configured archive-mode root and an idle session with no pending
permission and no running child; never approve permissions.
O1. archive_session {session_id: <idle session>}. Expect archived=true, changed=true, a session_archive
    audit line, no native write (the journal and the native request log show none).
O2. list_catalog without include_archived hides it; with include_archived:true it appears with
    archived:true. get_session is unchanged (it does not report the overlay).
O3. send_prompt to the archived session: FORBIDDEN/session_archived, no native call.
O4. unarchive_session: it returns to the default list; visibility restored, nothing else.
O5. close_session on a session with a running execution: the existing interrupt first, then archive;
    one session_close line (interrupt=interrupted), no extra interrupt line. If the interrupt fails or
    is unsettled, the session stays un-archived.
Report each expected result, the audit lines, the native request log (no PATCH/DELETE/POST for the
archive step; close_session has exactly the one abort POST), and any state presented as success
without evidence.
```
Corroboration: the archive/unarchive/close events (`session_archive`, `session_unarchive`,
`session_close`); the dashboard ring drops these events until its allowlist is amended
(`internal/dashboard/audit.go:32`); the overlay file's entries/generation and the journal counters.
Live behaviour is UNVERIFIED (the S6 tests use a fake native).

Rollback: unset `BRIDGE_OVERLAY_DIR`, restart; the tools disappear; the file remains on disk (labels
are only read when the store is configured).

## Stage 5: reject-only permission relay (R5b-style)

Switch: `BRIDGE_PERMISSION_RELAY` — unset/`reject_only` registers `reply_permission` (the default by
D15 Q3); `off` keeps the 11-tool surface and installs nothing; `policy` or any other value refuses
startup (`cmd/bridge/relay.go:17-32`). `allow_once` is never offered (D12 exception (b), D15 Q1).
On **0672867** the source is not installed (PR #306 is open), so every call fails
`UPSTREAM_FAILURE` before any attempt line; after #306 the source exists but has **no native
incarnation evidence**, so every request stays `unattributed` and the tool refuses
`FORBIDDEN`/`not_verified`, with no native write and no attempt line (`docs/CONTRACTS.md` S6; PR
#306). The verified path remains UNVERIFIED and needs a maintainer decision on the incarnation
source (BACKLOG). The `docs/deploy/PERMISSION_RELAY_RUNBOOK.md` referenced by CONTRACTS is part of
PR #306 and is not on 0672867.

Setup/precheck:
1. Leave `BRIDGE_PERMISSION_RELAY` unset (or `reject_only`); `tools/list` shows 12 tools including
   `reply_permission`; the wiring comment records the no-incarnation limit
   (`cmd/bridge/relay.go:34-37`); no startup line reports it.
2. Make the native instance ask for a permission (for example `OPENCODE_PERMISSION={"bash":"ask"}`
   as in R5b, `docs/evidence/R5B_RUNBOOK.md` Step 1) and have a human ready to answer natively.

Acceptance conversation (R5b-style; the expected result today is the refusal):
```text
H5 stage 5 relay. Never approve a native permission; the human answers in OpenCode.
P1. send_prompt key h5-p1-<random> "Using your bash tool, run exactly: echo H5-PERM-PROBE . Report its
    output." Poll get_execution_status until waiting_for_permission; record get_session
    pending_permission_count (1) and, on a build with a source, whether pending_permission is absent.
P2. reply_permission {execution_id, permission_id, decision: "reject"}. Expected today:
    FORBIDDEN/not_verified (after #306) or UPSTREAM_FAILURE (0672867) — never a native reply. Then
    reply_permission with decision "allow_once": FORBIDDEN/allow_once_disabled, zero native calls.
P3. The human rejects the request natively in OpenCode; poll the execution to terminal/unknown,
    get_execution_result and read_session_history. H5-PERM-PROBE must never appear in tool output.
Report the tool results, the native request log (no POST /permission/*/reply), the journal line for
the refused call, and the final guard/state.
```
Corroboration: the journal has one `event=permission_reply phase=refused` line with the closed code
(after #306) and no `phase=attempt`; on 0672867 the refusal is `UPSTREAM_FAILURE` and no line is
written before the source exists (the manager's reply path returns before the attempt). The native
permission is answered by the human; no Bridge approval. **R5b/R5 live acceptance remains
NOT_EXERCISED**; the `reply_permission` verified path needs the incarnation source.

Rollback: set `BRIDGE_PERMISSION_RELAY=off`, restart; the tool disappears (11 tools) and the replier,
source and attempt writer are not installed.

## Stage 6: Claude Code adapter — last, and OFF until ADR-021's live acceptance passes

**Do not enable this in H5 until the ADR-021 driver task and the live acceptance (R6) have passed.**
`docs/deploy/CLAUDE_CODE_RUNBOOK.md` is reference only and its live behaviour is UNVERIFIED; the
credential canary has not run (D14 Q10). With `BRIDGE_CLAUDE_COMMAND` unset, every
`BRIDGE_CLAUDE_*` value must also be unset or the Bridge refuses to start (no partial enablement,
`cmd/bridge/claude.go:40-47`). A read-only ingress ignores the adapter (`claude.go:85-95`).

Required when enabling (all validated at start, `cmd/bridge/claude.go:39-80`; names/defaults at
`:16-30`):
- `BRIDGE_CLAUDE_COMMAND` — absolute path of the root-owned wrapper (required to enable; the
  `hb-p6-claude`/`sudo -n -u hbclaude -H -- …` pattern, `docs/deploy/CLAUDE_CODE_RUNBOOK.md` §1).
- `BRIDGE_CLAUDE_SESSIONS` — absolute allowlist JSON, mode `0600`.
- `BRIDGE_CLAUDE_VERSIONS` — exact `claude --version` list, comma separated, **no default**; only
  `2.1.322` was exercised in the spike (runbook §4).
- `BRIDGE_CLAUDE_HISTORY_DIR` — private directory (absolute).
- `BRIDGE_CLAUDE_HOME` — the Claude service user's (`hbclaude`) HOME, absolute.
- `BRIDGE_CLAUDE_MODEL` — model name.
- `BRIDGE_WRITER_LOCK_DIR` — the existing lock directory (shared with OpenCode).
- `BRIDGE_ALLOWED_ROOTS` / `BRIDGE_ALLOWED_SESSIONS` — each `claudecode:<uuid>` must also be in both
  the allowlist file and the ACL (`docs/deploy/CLAUDE_CODE_RUNBOOK.md` §5).
- Optional: `BRIDGE_CLAUDE_LANG` (default `C.UTF-8`, `internal/claudewire/harness.go:28,73`),
  `BRIDGE_CLAUDE_MAX_TURNS` (1–1000, default 20, `cmd/bridge/claude.go:38-39,63-67`),
  `BRIDGE_CLAUDE_MAX_BUDGET_USD` (decimal, optional), `BRIDGE_CLAUDE_RUN_MAX` (1s–24h, default 15m,
  `internal/adapters/claudecode/runner/executor.go:102`).

Host prerequisites (operator, from the runbook): two non-root users (`harness-bridge`, `hbclaude`);
the root-owned wrapper and the sudoers entry; `/etc/claude-code/managed-settings.json` with
`allowManagedHooksOnly`, `allowManagedMcpServersOnly`, `allowManagedPermissionRulesOnly` and the
Read/Glob/Grep deny rules on `~/.claude/**` and `~/.config/anthropic/**`; the Claude user's
`cleanupPeriodDays` at least the Bridge retention. **`BRIDGE_CLAUDE_HISTORY_MAX_AGE` retention is not
wired** (#266 P3-4, BACKLOG); the 30-day statement in the runbook is the intended default, not an
implemented switch.

Acceptance (only after the live acceptance): a `claudecode:` session in the allowlist and ACL;
send_prompt, continuity, replay, busy/interrupt; a release of an `unknown` execution only through
`admin release-guard` after the child and its process group are confirmed gone. Enabling before the
live acceptance is a STOP.

Rollback: unset **all** `BRIDGE_CLAUDE_*` values (a partial set refuses startup), restart; keep the
wrapper/sudoers/managed settings installed.

## Corroboration patterns (journal, dashboard, native)

- Journal (same file as every AUDIT-01 line): `event=prompt_submission|interrupt|guard_release`
  (existing, H4), `event=session_create` (stage 3), `event=permission_reply` with
  `phase=attempt|result|refused` (stage 5), `event=session_archive|session_unarchive|session_close`
  (stage 4). Free-text-free; `idem` is 12 hex; `decided_by=chat_client`, `human_verified=false` on
  permission lines (`internal/audit/attempt.go`, `internal/mcp/audit.go`).
- Dashboard: `/audit` shows only the ring allowlist (`guard_release`, `prompt_submission`,
  `interrupt`, `session_create`; `internal/dashboard/audit.go:32`); `permission_reply` and the
  archive events are journal-only until that allowlist is amended.
- Native (Worker, read-only DB copy as in #73/#76): the expected user messages, the marker file, the
  workspace diff, the permission request/reply rows; never approve or replay.

## Global rollback

1. Remove `zzzz-h5.conf`, `daemon-reload`, restart: `zzz-h4.conf` runs the H4 binary.
2. No feature switch survives a drop-in removal; if the H4 build does not support a value, remove it
   as well (the H4 notes' `zzz-h4.conf` is the reference).
3. Rotate the token: H4/R5 constraints (R5 `692992d` caps at 24 h; H4 supports 168 h).
4. Re-add the H4 connector; remove the H5 one.
5. Keep the workspace and native sessions; `h5/` is the only expected new tree.

## Open questions for the Planning Desk / maintainer

1. **Lifecycle and overlay wiring are not done.** The ADRs are accepted, but no `cmd/bridge` code
   reads `BRIDGE_LIFECYCLE_CONFIG` or constructs `ArchiveConfig`/`Server.Catalog`; stages 3 and 4
   are NOT AVAILABLE on 0672867 and cannot be enabled by any value. H5 for those stages needs the
   wiring task(s) in the freeze (and #307 for the overlay).
2. **Relay registration default is `reject_only`, not off** (D15 Q3), while this task's general rule
   is "each default OFF". `off` is the opt-out; the tool is registered but cannot answer without an
   incarnation source. Is the registration acceptable for the H5 stage-1 surface (12 tools)?
3. **No native incarnation source exists**, so the relay's verified path is not live-testable; the
   retain/reject decision and the source design are the maintainer's (BACKLOG, PR #306).
4. **`BRIDGE_OVERLAY_DIR`, the overlay bounds and the `session_archived` reason are proposals**, not
   decisions (ADR-025 Q10/Q6; BACKLOG).
5. **R-DASH, R5b and the Claude live acceptance remain NOT_EXERCISED/UNVERIFIED**; each stage's
   pass criterion is evidence, not a claim.
6. The exact freeze SHA and whether #306/#307 are merged before it are the Planning Desk's call;
   re-check every "NOT AVAILABLE" row against that SHA before the operator starts.
