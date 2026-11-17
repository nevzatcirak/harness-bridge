# Session lifecycle tools: operator runbook (ADR-022, ADR-025)

Status: **the tools exist in the code but are NOT wired into `cmd/bridge`.** A deployed Bridge registers
none of `list_catalog`, `create_session`, `archive_session`, `unarchive_session` or `close_session`, and
setting the variables below changes nothing today. This page says what the configuration will be, what the
tools do, and what is and is not verified. It is a file of its own (next to `PERMISSION_RELAY_RUNBOOK.md`,
`DASHBOARD.md` and `CLAUDE_CODE_RUNBOOK.md`) because lifecycle has its own configuration file, directory and
failure modes, and because H5 stages 3 and 4 refer to it; the tool contracts are in
`docs/CONTRACTS.md` ("ADR-022 S3", "S4", "S6 and ADR-025").

## What is true today (base `ebb2ce3`)

| Item | State | Source |
|---|---|---|
| `list_catalog` | registered only when `Server.Catalog != nil && Server.Catalog.Roots != nil` | `internal/mcp/server.go:330` |
| `create_session` | registered only when `Server.Create != nil` | `internal/mcp/server.go:335` |
| `archive_session`, `unarchive_session`, `close_session` | registered only when `Server.Archive != nil` | `internal/mcp/server.go:340` |
| Production code that sets those fields | **none**: `cmd/bridge` does not mention them | grep of `cmd/bridge/*.go` (non-test) |
| `BRIDGE_LIFECYCLE_CONFIG` | parsed by `lifecycle.FromEnv`, which no production code calls | `internal/lifecycle/config.go:141` |
| `BRIDGE_OVERLAY_DIR` | named by ADR-025 §9 Q5/Q6 (D18); **no code reads it** (it appears only in comments) | `internal/mcp/server.go:42`, `internal/mcp/archive.go:51` |
| Tool count of a deployed Bridge | eleven (twelve with the default `reject_only` relay) | `docs/CONTRACTS.md` |

Wiring the configuration, and reading `BRIDGE_OVERLAY_DIR`, are separate planner-scheduled tasks
(ADR-022 "As built (slice S6)", `:771-773`, ADR-025 §11 "Not wired in production", `:424-425`). Until they land, **do not set either variable and expect
a change**; the H5 runbook marks stages 3 and 4 NOT AVAILABLE for the same reason.

## The lifecycle configuration (for when it is wired)

`BRIDGE_LIFECYCLE_CONFIG` is the path of a JSON file. Unset or empty means lifecycle is off
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
| `roots[].modes` | non-empty subset of `create`, `archive`, no duplicates | `config.go:25-29,60-63` |
| `roots[].max_sessions` | default 20, a positive integer | `config.go:33-38` |
| `roots[].max_depth` | default 0 (only the root itself), not negative | `config.go:96-98,309-313` |
| `roots[].existing_tools` | default false; parsed only, grants nothing until the ADR-012 amendment (S9) | `config.go:99-102` |
| `create_per_hour`, `archive_per_hour` | default 10 each, positive; memory-only windows, a restart resets them | `config.go:33-38,259-284` |
| `max_total_sessions` | default 50, positive | `config.go:33-38,259-284` |

Lifecycle roots grant authority only to the lifecycle tools (ADR-022 3.2). A created session is **not** in
`BRIDGE_ALLOWED_SESSIONS`, and the existing tools do not act on it until the operator adds its ID to the ACL
(O2 is parsed but not implemented). `create_session` never creates a directory and refuses a root whose Git top
level is missing or outside the root (`non_git`, `not_in_root`).

## The archive overlay (for when it is wired)

Archive in Chat is a **Bridge-side label**, not a native archive (D16 Q1; the native `PATCH time.archived` stays
operator-only). The label lives in `overlay.json` in one directory (ADR-025 4.1, 4.2; `internal/overlay/overlay.go`):

- the directory (`BRIDGE_OVERLAY_DIR`, D18 Q5/Q6): absolute, a real directory (no symlink in the final component),
  owned by the service user, mode exactly `0700` (`overlay.go:207-233`); separate from the writer lock directory
  and the Claude history directory; not readable by agent users;
- files: `overlay.json` and `overlay.lock` (`overlay.go:36-39`); the writer takes the lock non-blocking, a second
  process runs as a reader without the archive tools; a leftover temporary file is removed by the lock holder;
- bounds: 1000 entries and 512 KiB, entries never dropped silently (`overlay.go:41-43`); the write is a temporary
  file, fsync, rename, directory fsync, then publish (`overlay.go:609-720`);
- an invalid file: the **writer refuses to start**; a read-only process starts and reports the overlay as
  unknown, so `list_catalog` fails with `INTERNAL` rather than listing a guess (D18 Q3);
- **unset: the archive tools are not registered; there is no memory-only mode** (D18 Q5).

Operating rules that already hold in the code: the overlay never grants or denies anything beyond hiding a
session and refusing `send_prompt` to it (`FORBIDDEN`, reason `session_archived`); a deliberate edit of the
file by the service user is out of the checksum's protection (ADR-025 §6, T1); recovery is file-level and manual
(move the file aside and restart, D18 Q9), after which the sessions are visible again.

## What the tools do (summary; the contract is in CONTRACTS)

| Tool | Writes | Notable refusals |
|---|---|---|
| `list_catalog` | nothing | `INVALID_INPUT` bad filter; `UPSTREAM_TIMEOUT` (10 s deadline); `INTERNAL` unknown overlay |
| `create_session` | one native `POST /session` per accepted request, never retried | `SESSION_LIMIT`, `IDEMPOTENCY_CONFLICT`, `UNKNOWN_OUTCOME` |
| `archive_session`, `unarchive_session` | the overlay file only; **no native write** | `SESSION_BUSY` (not idle, pending permission, running child, held guard), `SESSION_LIMIT` (hourly cap or full overlay), `UNKNOWN_OUTCOME` reason `overlay_uncertain` |
| `close_session` | the existing interrupt (at most one native abort POST), then the overlay | the same, plus `INVALID_INPUT` "execution already terminal" and `UNKNOWN_OUTCOME` when the interrupt does not settle in 5 s |

`close_session` **can interrupt a running execution and then still refuse the archive** (the cap, `SESSION_BUSY`,
an overlay failure); the interrupt is not rolled back. All five tools are unavailable under `BRIDGE_READ_ONLY`
except `list_catalog`. Every lifecycle mutation writes one AUDIT-01 line (`session_create`, `session_archive`,
`session_unarchive`, `session_close`) with no paths or free text; the dashboard ring keeps only
`session_create` until its allowlist is amended (`internal/dashboard/audit.go:32`).

## Enabling (not possible yet) and rollback

Today there is nothing to enable. When the wiring exists, the order is: create the configuration file (mode
`0600`, owned by the service user) and the overlay directory (`0700`), set the variables in the Bridge's
drop-in, restart, check `tools/list`, and run the stage 3 and stage 4 acceptance conversations of
`docs/deploy/H5_RUNBOOK.md`. Rollback is to unset the variables and restart: the tools disappear, created
sessions remain native sessions, and `overlay.json` stays on disk, unread.

## Verification status

Everything on this page is tested against fakes (a fake native server, the real overlay store on a temporary
directory). **No live run has been made**: the behaviour of an archived session on a real OpenCode, the
`POST /session` and abort calls against a real server, and the overlay under a real power loss are UNVERIFIED.
`BRIDGE_OVERLAY_DIR` and the file names are decided by D18 Q5/Q6, but the ADR still calls them proposals in §4.1
and §10 (recorded in `docs/BACKLOG.md`).
