# Session lifecycle tools: operator runbook (ADR-022, ADR-025)

> **Names (Cirak, ADR-026 S1).** The product is now called Cirak: the daemon is `cmd/cirak`, the binary `cirak`, and every environment variable is `CIRAK_*` (this text uses the new names). The legacy `BRIDGE_*` names are still read until v0.2.0, with one warning per variable that never prints a value; a `CIRAK_X` and a `BRIDGE_X` set to different values refuse to start, naming both variables; an empty value counts as unset on both sides. See `docs/adr/ADR-026-cirak-name-and-distribution.md` section 2.3. Host names, paths and unit names (`harness-bridge-*`, `/etc/harness-bridge`, `/opt/harness-bridge-test`) are unchanged.

Status: **wired into `cmd/cirak` and enabled by default (D22), inert until configured.** The five tools
`list_catalog`, `create_session`, `archive_session`, `unarchive_session` and `close_session` appear when
`CIRAK_LIFECYCLE_CONFIG` (and, for the archive tools, `CIRAK_OVERLAY_DIR`) is set and valid; with the variables
unset a deployed Bridge lists exactly the tools it listed before. An invalid value **refuses to start**, it never
starts "without lifecycle". This page says what the configuration is, what the tools do, and what is and is not
verified. It is a file of its own (next to `PERMISSION_RELAY_RUNBOOK.md`, `DASHBOARD.md` and
`CLAUDE_CODE_RUNBOOK.md`) because lifecycle has its own configuration file, directory and failure modes, and because
H5 stages 3 and 4 refer to it; the tool contracts are in `docs/CONTRACTS.md` ("ADR-022 S3", "S4", "S6 and
ADR-025", "Registration and production wiring").

## What the Bridge does at startup (`cmd/cirak/lifecycle.go`)

| Configuration | Result |
|---|---|
| neither variable set | inert: no lifecycle tool, the previous tool set (eleven, twelve with the default relay) |
| `CIRAK_LIFECYCLE_CONFIG` valid | `list_catalog` and `create_session` are registered |
| plus `CIRAK_OVERLAY_DIR` valid, writer holding the overlay lock | `archive_session`, `unarchive_session`, `close_session` too |
| `CIRAK_READ_ONLY=1` | `list_catalog` only; the overlay (if set) is opened as a reader (an invalid overlay file is then not a startup error: the state is unknown and `list_catalog` fails with `INTERNAL`); no mutating lifecycle tool. `send_prompt` is refused at call time on a read-only ingress, so no archived-session check is installed there |
| a second writer on the same overlay directory | no archive tool (it did not get the write lock, ADR-025 4.3); `list_catalog` and `create_session` stay. **It still refuses `send_prompt` to an archived session** (D18 Q1): the overlay is loaded, a poller refreshes the snapshot every 250 ms (the overlay re-reads a changed file at most once a second, ADR-025 Q8), so an archive made by the lock holder is refused here within about 1.25 s. An unreadable overlay file keeps the last good snapshot (ADR-025 4.4): archived sessions it knew stay refused, unrelated prompts are not blocked |
| `CIRAK_LIFECYCLE_CONFIG` invalid, naming no root, or `CIRAK_OVERLAY_DIR` invalid (relative, missing, symlink in the final component, not owned by the service user, not mode `0700`, or an invalid `overlay.json` for the writer) | **startup error** |
| `CIRAK_OVERLAY_DIR` set without `CIRAK_LIFECYCLE_CONFIG` | **startup error** (the overlay needs lifecycle roots) |

The startup log carries one line naming the registered lifecycle tools and the root count, for example
`lifecycle: registered list_catalog,create_session (roots=1, overlay=false)`; it names no path and no secret.
Tool counts with and without lifecycle configuration are in `docs/CONTRACTS.md` ("Registration and production
wiring"). ADR-012 authorization and the per-session ACL are unchanged: lifecycle roots grant nothing to the
existing tools, a created session is not in `CIRAK_ALLOWED_SESSIONS`, and there is no permanent delete.

## The lifecycle configuration

`CIRAK_LIFECYCLE_CONFIG` is the path of a JSON file. Unset or empty means lifecycle is off
(`config.go:141-148`). Checks at load, all fail-closed (`config.go:159-215`, constants `:23-70`):

- the path is absolute and clean, a regular file, with no symlink in any component, owned by the service user,
  mode exactly `0600`, at most 64 KiB, and the file that was opened is the file that was inspected;
- strict JSON: one object, unique keys, no unknown keys, no trailing data.

```json
{
  "roots": [
    {"path": "/opt/harness-bridge-test/h5", "modes": ["create", "archive"],
     "max_sessions": 20, "max_depth": 0, "existing_tools": false}
  ],
  "create_per_hour": 10,
  "archive_per_hour": 10,
  "max_total_sessions": 50
}
```

| Key | Meaning and default | Source |
|---|---|---|
| `roots[].path` | required; absolute, clean, no symlink alias, exists, a directory owned by the service user or root, not `/`, and not overlapping another root; at most 64 roots | `config.go:233-330` |
| `roots[].modes` | non-empty subset of `create`, `archive`, no duplicates | `config.go:25-29` (the two modes), `:350-365` (`checkModes`: empty, unknown or duplicate refused) |
| `roots[].max_sessions` | default 20, a positive integer | `config.go:33-38` |
| `roots[].max_depth` | default 0 (only the root itself), not negative | `config.go:96-98,309-313` |
| `roots[].existing_tools` | default false; parsed only, grants nothing until the ADR-012 amendment (S9) | `config.go:99-102` |
| `create_per_hour`, `archive_per_hour` | default 10 each, positive; memory-only windows, a restart resets them | `config.go:33-38,259-284` |
| `max_total_sessions` | default 50, positive | `config.go:33-38,259-284` |

Lifecycle roots grant authority only to the lifecycle tools (ADR-022 3.2). A created session is **not** in
`CIRAK_ALLOWED_SESSIONS`, and the existing tools do not act on it until the operator adds its ID to the ACL
(O2 is parsed but not implemented). `create_session` never creates a directory and refuses a root whose Git top
level is missing or outside the root (`non_git`, `not_in_root`).

### Host permissions of a root (H5 live findings F-H5-1 and F-H5-2)

- A root must be **owned by root or by the service user** (`internal/lifecycle/config.go:344-345`, reason
  `root_owner`): the Bridge refuses to start otherwise. A root owned by the agent user (live: the OpenCode
  user) is refused; the sitting made it `root:<agent-group>`, mode `0710`, with an ACL
  (`u:<agent-user>:rwx`) so that the agent keeps its write access.
- The service user needs **read and search (`r-x`) on the root**, `r-x` on the directory a session is created in,
  and read on its `.git`. The request path opens every root with `os.OpenRoot`
  (`internal/security/lifecycle_root.go:123`); with only search (`--x`) every create was `FORBIDDEN` with no reason.
  The Bridge now performs the same open at startup (`lifecycle_root.go:59`) and refuses to start with
  `lifecycle root "<path>" cannot be opened by the service user`; a create refused by a failed filesystem check
  of the directory or its Git metadata says so (`unavailable: a filesystem check could not complete ...`).
- Check as the service user without printing content: `namei -m <root>` and
  `sudo -u <service-user> sh -c 'test -r <root> -a -x <root> && echo readable'`.

## The archive overlay

Archive in Chat is a **Bridge-side label**, not a native archive (D16 Q1; the native `PATCH time.archived` stays
operator-only). The label lives in `overlay.json` in one directory (ADR-025 4.1, 4.2; `internal/overlay/overlay.go`):

- the directory (`CIRAK_OVERLAY_DIR`, D18 Q5/Q6): absolute, a real directory (no symlink in the final component),
  owned by the service user, mode exactly `0700` (`overlay.go:207-233`); separate from the writer lock directory
  and the Claude history directory; not readable by agent users;
- files: `overlay.json` and `overlay.lock` (`overlay.go:36-39`); the writer takes the lock non-blocking, a second
  process runs as a reader without the archive tools; a leftover temporary file is removed by the lock holder;
- bounds: 1000 entries and 512 KiB, entries never dropped silently (`overlay.go:41-43`); the write is a temporary
  file, fsync, rename, directory fsync, then publish (`overlay.go:609-720`);
- an invalid file: the **writer refuses to start**; a read-only process starts and reports the overlay as
  unknown, so `list_catalog` fails with `INTERNAL` rather than listing a guess (D18 Q3);
- under the unit's `ProtectSystem` the directory must be a `StateDirectory=` (with `StateDirectoryMode=0700`):
  a directory made by hand failed with `openat overlay.lock: read-only file system` (H5 live finding F-H5-3);
- **unset: the archive tools are not registered; there is no memory-only mode** (D18 Q5).

Operating rules that already hold in the code: the overlay never grants or denies anything beyond hiding a
session and refusing `send_prompt` to it (`FORBIDDEN`, reason `session_archived`); a deliberate edit of the
file by the service user is out of the checksum's protection (ADR-025 §6, T1); recovery is file-level and manual
(move the file aside and restart, D18 Q9), after which the sessions are visible again.

## What the tools do (summary; the contract is in CONTRACTS)

| Tool | Writes | Notable refusals |
|---|---|---|
| `list_catalog` | nothing (every row carries `archived`, `false` unless archived) | `INVALID_INPUT` bad filter; `UPSTREAM_TIMEOUT` (10 s deadline); `INTERNAL` unknown overlay |
| `create_session` | one native `POST /session` per accepted request, never retried; a replay returns the recorded output with `"replay": true` | `SESSION_LIMIT`, `IDEMPOTENCY_CONFLICT`, `UNKNOWN_OUTCOME`, `FORBIDDEN` (a failed filesystem check says `unavailable`) |
| `archive_session`, `unarchive_session` | the overlay file only; **no native write** | `SESSION_BUSY` (not idle, pending permission, running child, held guard), `SESSION_LIMIT` (hourly cap or full overlay), `UNKNOWN_OUTCOME` reason `overlay_uncertain` |
| `close_session` | the existing interrupt (at most one native abort POST), then the overlay | the same, plus `INVALID_INPUT` "execution already terminal" and `UNKNOWN_OUTCOME` when the interrupt does not settle in 5 s |

`close_session` **can interrupt a running execution and then still refuse the archive** (the cap, `SESSION_BUSY`,
an overlay failure); the interrupt is not rolled back. Under `CIRAK_READ_ONLY` only `list_catalog` is registered (the create and
archive tools are not). Every lifecycle mutation writes one AUDIT-01 line (`session_create`, `session_archive`,
`session_unarchive`, `session_close`) with no paths or free text; the dashboard ring keeps only
`session_create` until its allowlist is amended (`internal/dashboard/audit.go:32`).

## Enabling and rollback

Create the configuration file (mode `0600`, owned by the service user) and, for archive, the overlay directory
(`0700`, owned by the service user), set the variables in the Bridge's drop-in, restart, check the startup line and
`tools/list`, and run the stage 3 and stage 4 acceptance conversations of `docs/deploy/H5_RUNBOOK.md`. A bad value
stops the Bridge at start with the reason (config path, mode, root, overlay directory), so fix it and restart.
Rollback is to unset the variables and restart: the tools disappear, created sessions remain native sessions, and
`overlay.json` stays on disk, unread. **Archived sessions become visible in the catalog and dashboard and accept
`send_prompt` again**, because nothing reads the overlay any more; to keep a session hidden after a rollback,
archive it natively (operator-only) or leave the variables set.

## Verification status

Everything on this page is tested against fakes (a fake native server, the real overlay store on a temporary
directory), and the lifecycle tools were run **once live on 2026-11-21** (the H5 sitting,
`docs/evidence/R7_EVIDENCE_2026-11-21.md`): `list_catalog`, `create_session` against a real OpenCode (replay,
conflict, an invalid subdirectory refused), the ACL update and restart, prompts to the created session,
`archive_session` (hidden in the catalog and dashboard, `send_prompt` refused `session_archived`),
`unarchive_session` and `close_session` with its interrupt all behaved as specified, after the host findings
F-H5-1 to F-H5-3 were fixed. `get_session` stays unchanged by an archive label by design (ADR-025 Q2: the
existing tools are byte-identical whatever the overlay). The overlay under a real power loss and a second writer
on the same overlay directory are UNVERIFIED.
`CIRAK_OVERLAY_DIR` and the file names are decided by D18 Q5/Q6, but the ADR still calls them proposals in §4.1
and §10 (recorded in `docs/BACKLOG.md`).
