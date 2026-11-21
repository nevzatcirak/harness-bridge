# ADR-027: one config file and the `cirak init` setup wizard

Date: 2026-11-21. Status: **PROPOSED** (maintainer decision D27; acceptance under D19). It depends on
ADR-026 (Cirak rename, D26): `CIRAK_*` env names, with the legacy `BRIDGE_*` names accepted for one
minor release. Until this ADR is accepted the binary keeps its current flags and environment; until
ADR-026 is accepted the same design applies to the current name with `BRIDGE_*` only. It changes no
runtime authority: ADR-010/011/012/020/022/023/024/025 keep their contracts; the file only replaces
the *input* surface.

Base inspected: `main` `e2ac772` (2026-11-21). Nothing here was executed live.

## Problem

A first working deployment today needs about 34 environment variables and hand-made systemd
drop-ins: listener/TLS/token, the OpenCode client and credentials, the ACL, the writer lock and admin
socket, the lifecycle config and overlay, the permission relay, the dashboard, the Claude adapter,
and the limits. The live H5 sitting (#460) found five concrete traps: a lifecycle root owned by the
wrong user (F-H5-1), a service user without `r-x` on the root (F-H5-2), an overlay directory that is
read-only under `ProtectSystem` without `StateDirectory=` (F-H5-3), token rotation by hand (F-H5-4),
and a reproducible build polluted by an in-tree `GOCACHE`/`out` (F-H5-5). The maintainer decision
D27: an interactive `cirak init` wizard plus one config file, then `cirak serve`, so a first setup
takes minutes and the traps are checked before the first start.

This ADR decides the config file (format, location, precedence, schema, secret handling) and the
wizard/doctor/migration surface. It does not change the Bridge's behaviour at runtime.

## 1. Config file

### 1.1 Format

| Option | Dependency | Strengths | Costs |
| --- | --- | --- | --- |
| **JSON (`encoding/json`, stdlib)** | none | already used for the lifecycle config (strict duplicate/unknown-key checking exists, `internal/lifecycle/config.go`); no new dependency; unambiguous | no comments; hand-editing is less pleasant (the wizard writes it, `config check` explains every refusal) |
| TOML | one library (BurntSushi/toml or pelletier/go-toml) | comments, sections, human-friendly | new dependency (the project prefers minimal dependencies); another parser and error model to harden |
| YAML | one library (gopkg.in/yaml.v3) | comments, familiar to operators | largest parser/attack surface (anchors, aliases, multi-document, implicit typing); new dependency |

**Recommendation: JSON, stdlib only.** `cirak init` generates it, `cirak config check` validates it,
and this ADR is its reference. The example in §1.4 is the documented shape; a `--format toml` output
is deliberately out of the first slices. (Open question 1 asks whether TOML ergonomics outweigh one
dependency.)

### 1.2 Location and precedence

Resolution, highest first:

1. **Flag** — `--config <path>`, `--listen`, `--read-only`, `--token-file` (only settings with a real
   operational need for a flag; every other value comes from env/file). A flag that is also a secret
   location (`--token-file`) points at a file, never at the value.
2. **Environment** — `CIRAK_*` first, then the legacy `BRIDGE_*`/`OPENCODE_*` names for the ADR-026
   compatibility window. If both a new and a legacy name are set with **different** effective
   values, the binary refuses to start (fail closed); equal values are accepted with one warning per
   variable. No warning contains a value.
3. **Config file** — `--config` path if given (must exist and parse); otherwise
   `CIRAK_CONFIG`/`BRIDGE_CONFIG` if set (must exist); otherwise the first existing of
   `/etc/cirak/config.json` (system) and `$XDG_CONFIG_HOME/cirak/config.json` (falling back to
   `~/.config/cirak/config.json`). With no file the binary behaves exactly as today (env-only), so
   the live H5 drop-in keeps working without edits. No implicit file in the current directory.
4. **Default** — the compiled defaults (the same values the environment defaults use today:
   `127.0.0.1:8080`, `24h` token lifetime, `${OPENCODE_URL:-http://127.0.0.1:4096}`, `LANG=C.UTF-8`,
   `max_turns=20`, `run_max=15m`, and so on).

Precedence is per setting, not per file: a probe key list with the effective source (flag/env/file/
default) is part of `cirak doctor` and of a `--print-effective` mode of `config check` (secrets
shown as `set`, never as a value). A `--help` note states the order. Env interpolation
(`${VAR}`) inside the file is **not** supported in v1 (it invites surprising indirection and
secret expansion); the file holds literal paths.

### 1.3 Schema (every current environment variable)

The JSON has one object with the sections below; unknown keys, duplicate keys, wrong types, a
trailing document and a file over 1 MiB are refused. `null` and `""` differ: `""` is an explicit
unset, a missing key is the default. Paths are absolute and clean (no `..`, no symlink in any
component for secrets and the config itself).

| Section / key | Type | Env (new / legacy) | Default | Secret |
| --- | --- | --- | --- | --- |
| `server.listen` | string | `CIRAK_LISTEN` / `BRIDGE_LISTEN` | `127.0.0.1:8080` | no |
| `server.read_only` | bool | `CIRAK_READ_ONLY` / `BRIDGE_READ_ONLY` | `false` | no |
| `server.public_origin` | string | `CIRAK_PUBLIC_ORIGIN` / `BRIDGE_PUBLIC_ORIGIN` | `""` | no |
| `server.principal` | string | `CIRAK_PRINCIPAL` / `BRIDGE_PRINCIPAL` | `""` | no |
| `server.tls.cert_file` | string | `CIRAK_TLS_CERT` / `BRIDGE_TLS_CERT` | `""` | no (public cert) |
| `server.tls.key_file` | string | `CIRAK_TLS_KEY` / `BRIDGE_TLS_KEY` | `""` | **yes (file)** |
| `token.file` | string | `CIRAK_TOKEN_FILE` / `BRIDGE_TOKEN_FILE` | `""` | **yes (file)** |
| `token.credential` | string | `CIRAK_TOKEN_CREDENTIAL` (new; no legacy name) | `""` | **yes (systemd credential name)** |
| `token.expires` | RFC3339 | `CIRAK_TOKEN_EXPIRES` / `BRIDGE_TOKEN_EXPIRES` | `""` | no |
| `harness.opencode.url` | string | `CIRAK_OPENCODE_URL` / `OPENCODE_URL` | `http://127.0.0.1:4096` | no |
| `harness.opencode.username` | string | `CIRAK_OPENCODE_USERNAME` / `OPENCODE_SERVER_USERNAME` | `""` | no |
| `harness.opencode.password_file` | string | `CIRAK_OPENCODE_PASSWORD_FILE` / `OPENCODE_SERVER_PASSWORD_FILE` | `""` | **yes (file)** |
| `harness.opencode.password_credential` | string | `CIRAK_OPENCODE_PASSWORD_CREDENTIAL` (new; no legacy name) | `""` | **yes (credential)** |
| `harness.claude.command` | string | `CIRAK_CLAUDE_COMMAND` / `BRIDGE_CLAUDE_COMMAND` | `""` (adapter off) | no |
| `harness.claude.sessions_file` | string | `CIRAK_CLAUDE_SESSIONS` / `BRIDGE_CLAUDE_SESSIONS` | `""` | no (ACL, 0600) |
| `harness.claude.versions` | string | `CIRAK_CLAUDE_VERSIONS` / `BRIDGE_CLAUDE_VERSIONS` | `""` | no |
| `harness.claude.history_dir` | string | `CIRAK_CLAUDE_HISTORY_DIR` / `BRIDGE_CLAUDE_HISTORY_DIR` | `""` | no |
| `harness.claude.home` | string | `CIRAK_CLAUDE_HOME` / `BRIDGE_CLAUDE_HOME` | `""` | no |
| `harness.claude.model` | string | `CIRAK_CLAUDE_MODEL` / `BRIDGE_CLAUDE_MODEL` | `""` | no |
| `harness.claude.lang` | string | `CIRAK_CLAUDE_LANG` / `BRIDGE_CLAUDE_LANG` | `C.UTF-8` | no |
| `acl.roots` | []string | `CIRAK_ALLOWED_ROOTS` / `BRIDGE_ALLOWED_ROOTS` | `[]` | no |
| `acl.sessions` | []string | `CIRAK_ALLOWED_SESSIONS` / `BRIDGE_ALLOWED_SESSIONS` | `[]` | no |
| `writer.lock_dir` | string | `CIRAK_WRITER_LOCK_DIR` / `BRIDGE_WRITER_LOCK_DIR` | `""` | no |
| `writer.admin_socket` | string | `CIRAK_ADMIN_SOCKET` / `BRIDGE_ADMIN_SOCKET` | `""` | no |
| `lifecycle.config_file` | string | `CIRAK_LIFECYCLE_CONFIG` / `BRIDGE_LIFECYCLE_CONFIG` | `""` | no |
| `overlay.dir` | string | `CIRAK_OVERLAY_DIR` / `BRIDGE_OVERLAY_DIR` | `""` | no |
| `permission.relay` | enum | `CIRAK_PERMISSION_RELAY` / `BRIDGE_PERMISSION_RELAY` | `reject_only` | no |
| `permission.incarnation` | enum | `CIRAK_PERMISSION_INCARNATION` / `BRIDGE_PERMISSION_INCARNATION` | `off` | no |
| `dashboard.socket` | string | `CIRAK_DASHBOARD_SOCKET` / `BRIDGE_DASHBOARD_SOCKET` | `""` | no |
| `dashboard.addr` | string | `CIRAK_DASHBOARD_ADDR` / `BRIDGE_DASHBOARD_ADDR` | `""` | no |
| `dashboard.redact_extra` | []string | `CIRAK_DASHBOARD_REDACT_EXTRA` / `BRIDGE_DASHBOARD_REDACT_EXTRA` | `[]` | no |
| `limits.token_max_lifetime` | duration | `CIRAK_TOKEN_MAX_LIFETIME` / `BRIDGE_TOKEN_MAX_LIFETIME` | `24h` (ceiling `720h`) | no |
| `limits.claude_max_turns` | int 1–1000 | `CIRAK_CLAUDE_MAX_TURNS` / `BRIDGE_CLAUDE_MAX_TURNS` | `20` | no |
| `limits.claude_max_budget_usd` | decimal string | `CIRAK_CLAUDE_MAX_BUDGET_USD` / `BRIDGE_CLAUDE_MAX_BUDGET_USD` | `""` | no |
| `limits.claude_run_max` | duration 1s–24h | `CIRAK_CLAUDE_RUN_MAX` / `BRIDGE_CLAUDE_RUN_MAX` | `15m` | no |

The legacy inline secrets `BRIDGE_TOKEN` and `OPENCODE_SERVER_PASSWORD` stay accepted from the
**environment** during the compatibility window (with the mutual-exclusion rules already in
`cmd/bridge/secret.go:13-43`), but the **file has no inline-secret key**: `token.value` and
`harness.opencode.password` are unknown keys and are refused. This is deliberate (see §1.5).

Two invariants the validators must keep, because they are existing contracts: `dashboard.socket` and
`dashboard.addr` are mutually exclusive; a TLS cert/key pair is all-or-nothing; `token.file` and
`token.credential` are mutually exclusive (and each is exclusive with a legacy inline `BRIDGE_TOKEN`);
lifecycle/overlay and Claude enablement keep their all-or-nothing rules.

Example (the wizard writes this shape; comments are not JSON, they annotate the ADR only):

```json
{
  "server": {"listen": "127.0.0.1:8080", "read_only": false,
             "public_origin": "", "principal": "",
             "tls": {"cert_file": "", "key_file": ""}},
  "token": {"file": "/etc/cirak/token", "credential": "",
            "expires": "2026-11-28T13:07:47Z"},
  "harness": {
    "opencode": {"url": "http://127.0.0.1:4096", "username": "",
                 "password_file": "/etc/cirak/opencode-password",
                 "password_credential": ""},
    "claude": {"command": "", "sessions_file": "", "versions": "",
               "history_dir": "", "home": "", "model": "", "lang": "C.UTF-8"}
  },
  "acl": {"roots": ["/opt/harness-bridge-test/workspace"],
          "sessions": ["opencode:ses_example"]},
  "writer": {"lock_dir": "/run/cirak/lock",
             "admin_socket": "/run/cirak-admin/admin.sock"},
  "lifecycle": {"config_file": "/etc/cirak/lifecycle.json"},
  "overlay": {"dir": "/var/lib/cirak/overlay"},
  "permission": {"relay": "off", "incarnation": "off"},
  "dashboard": {"socket": "/run/cirak-dashboard/dash.sock", "addr": "",
                "redact_extra": []},
  "limits": {"token_max_lifetime": "168h", "claude_max_turns": 20,
             "claude_max_budget_usd": "", "claude_run_max": "15m"}
}
```

### 1.4 Secrets never inline

- A secret is only a **file reference** or a **systemd credential name**. The file must be a private
  regular file (mode `0600`, no symlink, 1–16384 bytes for a token; the OpenCode password has the
  same rule), owned by the process UID; a credential is resolved from
  `$CREDENTIALS_DIRECTORY/<name>` only when systemd set it.
- The config file itself: absolute canonical path, no symlink in any component, regular file, owned
  by the process UID (or root when the process runs as root), mode exactly `0600`; the parent
  directory not group/world-writable. `cirak config check` refuses otherwise, before any listener.
- `CIRAK_TOKEN` (inline) is never written by the wizard and is never a config key; `init`/`import`
  refuse to persist an inline secret and print the `install -m 0600` command instead (no value in
  the output).
- Strict unknown/duplicate-key rejection and no env interpolation; a hostile file can only fail
  closed.

### 1.5 `cirak config check`

Static validation, no network, no listener: parse and strict-check; every field's existing validator
(reusing the runtime validators, not a copy); config/token/password/TLS file modes and ownership;
token length and expiry (against the injected clock); root ownership `root` or the service user,
canonical path, and the service user's `r-x` via `stat`/ACL (an optional `--as <user>` runs the
read-only access probe); lifecycle file rules; overlay directory rules; dashboard socket parent;
Claude files/versions shape; unknown `CIRAK_*`/`BRIDGE_*` environment names as warnings (names only).
Output is a redacted summary (each secret `set`/`unset`), exit `0` clean, `2` refused, `1` warnings.
This is the command an operator runs before `systemctl restart` and in CI.

## 2. `cirak init`

Interactive by default (TTY), with a non-interactive `--yes` path whose non-secret answers are flags
(`--listen`, `--root` (repeatable), `--opencode-url`, `--service-user`, `--config`, `--token-file`,
`--expires`, `--remote`/`--local`, `--tls-cert`/`--tls-key`, `--install-service`, `--no-service`,
`--force`). `--yes` never prompts; missing non-secret input is an error; a token is generated only
if `--token-file` names a new file.

1. **Detect OpenCode.** Probe the configured URL with the same bounded reads the Bridge uses
   (`GET /session/status`, credentials from the prompt/file); on 200 record reachability and, best
   effort, the version (the exact version endpoint is UNVERIFIED; otherwise print `unknown`); on 401
   ask for credentials (echo off) and retry; on connection failure print the URL and the
   `systemctl --user status opencode` hint. Nothing is installed or changed.
2. **Ask for workspace roots** (repeatable) and **check the live ownership/permission rules**:
   - the root must exist, be canonical, and be owned by `root` or the service user
     (`internal/lifecycle/config.go:344-345`; F-H5-1);
   - the service user needs `r-x` on the root and on the created directories, and read on the `.git`
     it will use (`internal/security/lifecycle_root.go`; F-H5-2) — checked with `stat`/ACL and,
     optionally, `sudo -u <service-user> test -r/-x` (the operator sees the probe);
   - on failure print the **exact** fix, e.g.
     `chown root:<group> <root> && chmod 0710 <root> && setfacl -m u:<service-user>:rwx <root>`
     (and the `install -d -o … -g … -m …` for a create target), and never apply it silently; the
     wizard asks before printing a command that needs root.
3. **Generate a token** (default 64 chars, ≥43; file mode `0600`; expiry default `+7 days`, and the
   wizard writes `limits.token_max_lifetime: "168h"` explicitly so the pair is consistent; `--expires`
   must stay within it). The value is printed **once, only on a TTY**, with a warning, or written and
   its path printed in `--yes` mode; never in the config, a log, or an error.
4. **Write the config** (§1.3) mode `0600`, owned by the service user (root when `User=` is empty),
   plus the lifecycle JSON if roots were offered (mode `0600`). `--force` is required to replace an
   existing config; a backup is made (`config.json.bak-<timestamp>`, mode `0600`).
5. **Optionally install a service** (only with `--install-service`, and root is required and stated
   up front; never implied):
   - **Linux/systemd:** unit `cirak.service` with the proven hardening — `User=`/`Group=`,
     `ExecStart=/usr/local/bin/cirak serve`, `Environment=CIRAK_CONFIG=/etc/cirak/config.json`,
     `StateDirectory=cirak` + `StateDirectoryMode=0700` (overlay/history), `RuntimeDirectory=cirak-admin
     cirak-dashboard` + `RuntimeDirectoryMode=0700`, `LoadCredential=mcp-token:/etc/cirak/token`
     (config `token.credential: "mcp-token"`), `ProtectSystem=strict`, `NoNewPrivileges=yes`, `PrivateTmp=yes`,
     `RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6` (F-H5-3; the H5 systemd findings).
     `StateDirectory=` is writable without a separate `ReadWritePaths`. `ProtectHome=read-only` only
     when the Claude adapter is off: with it on, the child runs under `sudo -u hbclaude` in this
     unit's cgroup and writes its HOME, so the unit uses `ProtectHome=off` (or
     `ReadWritePaths=<claude home>`), and the wizard says which it chose. The unit is written, not
     started, unless the operator confirms; `systemctl daemon-reload` and the
     restart hint are printed.
   - **macOS/launchd:** a `LaunchAgent` (`~/Library/LaunchAgents/dev.cirak.agent.plist`) for the
     user, or a `LaunchDaemon` (`/Library/LaunchDaemons`) only when the operator asks and root is
     available; `KeepAlive`, `RunAtLoad`, `StandardOutPath`/`StandardErrorPath` under
     `~/Library/Logs/cirak` (or `/var/log/cirak`), `EnvironmentVariables` with `CIRAK_CONFIG`; no
     `LoadCredential` equivalent, so the token is a `0600` file owned by the run user.
   - It **never needs root** unless installing a *system* unit/daemon; a user-level setup runs
     entirely as the operator. The wizard says which case applies before asking for anything.
   - Token source follows the chosen install: with the systemd unit, the config keeps
     `token.credential: "mcp-token"` (and `token.file: ""`) so `LoadCredential` is the only source;
     without a unit (or on macOS), it keeps `token.file` with the `0600` file. The two are mutually
     exclusive, and `config check` refuses both set.
6. **Print the Claude connector values**: the listener URL (or the remote origin), the header name
   `Authorization`, and the token **once on a TTY** with `set +o history` and a warning; in `--yes`
   mode only the token file path. Then print the smoke steps: `cirak config check` → start →
   `cirak doctor --smoke` (a bounded `initialize` + `tools/list` against the local MCP endpoint;
   `tools/list` must show the expected surface, e.g. 11 tools with the relay off).

`cirak init` does not start the long-running service by default; it writes and prints the one
command to start it. Re-running it on an existing config asks before changing anything.

## 3. `cirak doctor`

Read-only diagnostics, safe to paste, no secret values (each is `set`/`unset` or an expiry timestamp):

1. **Config** — path, source of each effective setting (flag/env/file/default), strict-check result,
   file mode/owner, token/credential presence and expiry (no value), TLS cert/key presence and
   expiry (no content).
2. **OpenCode** — reachability, the same status/history reads the Bridge makes, any 401/403 as a
   credential problem, version if available.
3. **Sockets** — admin/dashboard paths, parent modes, `srw-------`/`drwx------`, stale-socket note;
   listener bound/loopback check; remote/TLS consistency.
4. **Lifecycle/overlay** — lifecycle file rules, each root's owner, mode and the service user's
   `r-x`, `.git` readability, overlay directory mode/`StateDirectory`/writability (F-H5-1..3).
5. **ACL/Claude** — allowed roots/sessions shape and overlap, `claudecode:` enablement all-or-none,
   Claude version file, wrapper path/mode (without reading the wrapper content).
6. **Environment** — a list of `CIRAK_*`/`BRIDGE_*` variables in effect (names only), unknown names
   as warnings, the new/legacy conflict check.
7. Optional `--smoke`: the bounded local MCP handshake above against an **already-running** Bridge
   (a client call: `initialize` + `tools/list`, one prompt-free request); never a prompt, never a
   mutation.

Exit `0` all checks pass, `1` warnings, `2` a check failed. It never starts a listener, never writes,
never restarts anything, and never includes a token, a password or a free-text provider error.

## 4. Local-only and remote modes

- **Local (default):** `server.listen 127.0.0.1:8080`, no TLS, dashboard on a unix socket, token
  still required (the connector is local but a token keeps the surface closed), admin socket under
  `/run/cirak-admin`, overlay under `StateDirectory=cirak`. `init` sets these.
- **Remote:** TLS cert/key (or a reverse proxy that terminates TLS and forwards to loopback),
  `public_origin` and `principal`, a token with an explicit expiry, and the connector URL from the
  public origin. `init --remote` asks for the cert/key and the origin, warns that a TLS cert and key
  are both required (never a plaintext fallback), and points at the existing Caddy/Traefik guidance
  (`docs/deploy/Caddyfile.example`, `docs/deploy/TRAEFIK_PRIVATE_BACKEND.md`). It never opens a
  firewall or edits a proxy; those stay operator steps.

## 5. Migration from the current env/drop-in deployment

The live H5 host must keep working with no edits. ADR-026 keeps `BRIDGE_*` accepted for one minor
release, so the migration is opt-in and reversible:

1. Run the new binary **read-only**: `cirak doctor` (no config) reports the effective environment
   and the five live checks without starting anything.
2. `cirak config import --env --out /etc/cirak/config.json` (mode `0600`) writes the non-secret
   settings as §1.3; a legacy inline `BRIDGE_TOKEN`/`OPENCODE_SERVER_PASSWORD` is **not** written —
   the command prints the `install -m 0600` path to move it to a file/credential and refuses to
   inline it. `--reject-unknown` makes any unrecognised `CIRAK_*`/`BRIDGE_*` name an error.
3. `cirak config check --config /etc/cirak/config.json`, then `cirak doctor --config …` and compare
   the effective settings with the env-only run (the doctor prints both side by side, secrets as
   `set`/`unset`).
4. Add `Environment=CIRAK_CONFIG=/etc/cirak/config.json` (or `EnvironmentFile=`) to the unit, keep the
   legacy drop-in for one restart, and confirm `tools/list` and the smoke check are unchanged.
5. Remove the migrated `BRIDGE_*` lines after the compatibility window; the config file is now the
   source. Rollback before step 5 is: remove `CIRAK_CONFIG`, restart; the legacy env still works.

## 6. Test plan (RED-first, no live services)

Unit tests with a fake FS/clock/exec and golden files; nothing starts a real listener or service.

1. **Format/strictness:** unknown key refused; duplicate key refused; wrong type; trailing document;
   >1 MiB; `token.value`/`password` refused; comments are invalid JSON (documented).
2. **Precedence:** flag > env > file > default per setting; new/legacy env with different values
   refused; equal values accepted with exactly one warning per variable; no implicit cwd file;
   `--config` missing is an error; no file = today's env-only behavior.
3. **Secrets:** config/token modes and owners refused when wrong; a symlinked config/token refused;
   inline secrets impossible; credential resolution only from `$CREDENTIALS_DIRECTORY`; no secret
   in `config check`/`doctor` output (grep the golden output for a canary token).
4. **Schema validators:** every existing rule (mutual exclusions, TLS pair, relay/incarnation enum,
   lifecycle/overlay all-or-none, Claude all-or-none, redact bounds) is reused, not reimplemented;
   a config that the env can express is accepted, an invalid one is refused with the same class of
   error.
5. **Init:** token generation length/mode/expiry; no chown/setfacl is applied (a fake exec records
   zero privileged commands); the exact fix command is printed; `--yes` without answers fails; an
   existing config needs `--force`; a backup is made; the printer shows the token only on a TTY and
   never in evidence; unit/plist rendering contains the hardening lines and no secret.
6. **Doctor:** each check's pass/fail with fakes; redaction (no token/password/free text); exit
   codes; unknown env names warned; `--smoke` performs one bounded `initialize`+`tools/list` and
   nothing mutating.
7. **Migration:** env-only vs imported config produce the same effective settings (a table-driven
   equivalence test); inline secrets are refused/preserved as file references; rollback works in the
   test by removing the config.
8. **Compatibility:** the H5 drop-in's legacy `BRIDGE_*` env still starts the binary with no config
   (the golden test is the current effective defaults), and the renamed `CIRAK_*` wins when both are
   equal.

Mutations (must be killed): precedence swapped; conflict accepted; unknown key ignored; secret
persisted inline; config mode unchecked; init applies `chown` silently; token printed in `--yes`/to
a log; unit drops `StateDirectory`/`LoadCredential`; doctor prints a value; `--smoke` sends a
prompt; migration writes the legacy inline token.

## 7. Slices

1. **S1 — config package and `config check`:** JSON schema, strict parsing, precedence (flag/env/file/
   default) with the ADR-026 compatibility warning/refusal, secret file rules, reuse of the runtime
   validators. Docs + CONTRACTS note. No wizard.
2. **S2 — `cirak init` core:** detection, root/ACL checks and exact fix output, token generation,
   config + lifecycle write, connector values and the smoke instructions; no service install yet.
3. **S3 — service install and `doctor`:** systemd unit (proven hardening) and launchd plist
   rendering/install; `doctor` static + live checks; `--smoke`.
4. **S4 — migration and remote mode:** `config import`, the side-by-side doctor, remote/TLS wizard
   path, runbook updates; the H5 host migration is an operator step, never run by a worker.
5. **S5 — rename integration:** `cmd/cirak`, `CIRAK_*` names and the legacy aliases land with
   ADR-026; no config schema change (the keys are name-neutral).

Each slice is one PR, RED-first, with the cases and mutations it owns; no slice needs a live service
for acceptance.

## 8. Open questions for the maintainer

1. **Format:** JSON stdlib (recommended) or accept one TOML dependency for comments/ergonomics?
2. **Config location:** `/etc/cirak/config.json` + `$XDG_CONFIG_HOME/cirak/config.json`, or a single
   `/etc/cirak/config`? Is a repository-local config ever allowed (recommended: only with `--config`)?
3. **Lifecycle roots:** keep the separate lifecycle JSON referenced by `lifecycle.config_file`
   (recommended, no contract change), or embed `roots` in the main file and deprecate the separate
   file?
4. **Service user:** create a dedicated `cirak` system user (packages) or run as the operator's user
   (local-first)? This decides the unit's `User=` and the token/root ownership the wizard checks.
5. **Token defaults:** 24h or 7 days, and is 64 chars the default? The H5 sitting used an in-place
   rotation with `LoadCredential`; should `init` default to a credential rather than a file?
6. **macOS default:** LaunchAgent (user, no root) vs LaunchDaemon (system) — recommended Agent unless
   asked.
7. **OpenCode version detection:** is there a supported version endpoint, or is "unknown" acceptable
   in `init`/`doctor`? (UNVERIFIED; the probe uses the Bridge's existing session/status reads.)
8. **Root permission check:** is an optional `sudo -u <service-user> test -r/-x` acceptable in the
   wizard (it is a read-only host probe) or must the check stay `stat`/ACL-only?
9. **`--smoke`:** is a temporary local MCP `initialize`+`tools/list` against a not-yet-started service
   acceptable, or should it only run against an already-running service (a client call, no listener)?
10. **Rename timing:** ship S1–S4 under the current binary name with `BRIDGE_*` only and add
    `CIRAK_*` in S5, or wait for ADR-026 and ship the `CIRAK_*` surface from S1?

## Out of scope

- Any runtime behavior, authority, guard, audit or ADR-010/011/012/024/025 change.
- A package manager or installer beyond ADR-026's distribution (this ADR is the config/wizard
  surface only).
- Editing the live H5 host, systemd, `/etc`, the connector or a proxy; every migration step is an
  operator action.
- A web UI for setup; `cirak init` and `doctor` are terminal commands.
