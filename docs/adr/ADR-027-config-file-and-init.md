# ADR-027: one config file and the `cirak init` setup wizard

Date: 2026-11-21. Status: **ACCEPTED (2026-11-21, maintainer decision D30, recorded in issue #117)**. The
maintainer decision D27 (an interactive `cirak init` wizard plus one config file) is implemented by this
ADR; acceptance is under D19. It depends on **ADR-026** (Cirak rename, D26, **accepted, D29, on main
`daac8de`**): the `CIRAK_*` env names are in force with the legacy `BRIDGE_*` names accepted for one
minor release; the `OPENCODE_*` names stay unrenamed (upstream). Acceptance changes no code by itself:
until the slices land, the binary keeps its current flags and environment; the config file only replaces
the *input* surface. It changes no runtime authority: ADR-010/011/012/020/022/023/024/025 keep their
contracts. The one **runtime exception** is the S1 remote-mode trigger: a token expiry (`token.expires`)
no longer implies a remote transport (P2-1 below); the transport is remote only with a public
origin/principal or TLS. That amends ADR-019's **trigger** sentence only (its token-lifetime contract and
the remote boundary—TLS, host, origin, path, per-request expiry—are unchanged), and local mode keeps
having no runtime expiry (ADR-019). The token/guard/audit authority is unchanged.

Base inspected: `main` `daac8de` (the #473 citations were re-checked there; the code did not change for
them). Nothing here was executed live.

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
wizard/doctor/migration surface. It does not change the Bridge's behaviour at runtime, except the
single remote-mode trigger stated above (S1) and the existing authority contracts.

## 1. Config file

### 1.1 Format

| Option | Dependency | Strengths | Costs |
| --- | --- | --- | --- |
| **JSON (`encoding/json`, stdlib)** | none | already used for the lifecycle config (strict decoding: `DisallowUnknownFields` at `internal/lifecycle/config.go:244`, trailing data refused at `:249`, duplicate keys refused by `checkStrictKeys`); no new dependency; unambiguous | no comments; hand-editing is less pleasant (the wizard writes it, `config check` explains every refusal) |
| TOML | one library (BurntSushi/toml or pelletier/go-toml) | comments, sections, human-friendly | new dependency (the project prefers minimal dependencies); another parser and error model to harden |
| YAML | one library (gopkg.in/yaml.v3) | comments, familiar to operators | largest parser/attack surface (anchors, aliases, multi-document, implicit typing); new dependency |

**Decided (D30 Q1): JSON, stdlib only.** `cirak init` generates it, `cirak config check` validates it,
and this ADR is its reference. The example in §1.3 is the documented shape; a `--format toml` output
is not in the first slices.

### 1.2 Location and precedence

Resolution, highest first:

1. **Flag** — `--config <path>`, `--listen`, `--read-only`, `--token-file` (only settings with a real
   operational need for a flag; every other value comes from env/file). A flag that is also a secret
   location (`--token-file`) points at a file, never at the value.
2. **Environment** — `CIRAK_*` first, then the legacy `BRIDGE_*` names for the ADR-026 compatibility
   window. The **`OPENCODE_*` names are not renamed and are not legacy** (upstream names, ADR-026
   §2.1/§2.2); they are read as they are. The new `CIRAK_CONFIG`, `CIRAK_TOKEN_CREDENTIAL` and
   `CIRAK_OPENCODE_PASSWORD_CREDENTIAL` names are new (no legacy twin). If both a new and a legacy
   `BRIDGE_*` name are set with **different** effective values, the binary refuses to start (fail
   closed); equal values are accepted with one warning per variable. No warning contains a value.
3. **Config file** — `--config` path if given (must exist and parse); otherwise **`CIRAK_CONFIG`**
   if set (must exist; this name is new and has no legacy twin); otherwise the first existing of
   `/etc/cirak/config.json` (system) and
   `$XDG_CONFIG_HOME/cirak/config.json` (falling back to `~/.config/cirak/config.json`). With no
   file the binary behaves exactly as today (env-only), so the live H5 drop-in keeps working without
   edits. No implicit file in the current directory. **Every start logs the config path in use**
   (never a value); a service run that finds only a per-user `$XDG_CONFIG_HOME` file logs that path
   and a warning (see the open limitation in §5).
4. **Default** — the compiled defaults (the same values the environment defaults use today:
   `127.0.0.1:8080`, `24h` token lifetime, `${OPENCODE_URL:-http://127.0.0.1:4096}`, `LANG=C.UTF-8`,
   `max_turns=20`, `run_max=15m`, and so on).

Precedence is per setting, not per file: a probe key list with the effective source (flag/env/file/
default) is part of `cirak doctor` and of a `--print-effective` mode of `config check` (secrets
shown as `set`, never as a value). A `--help` note states the order. Env interpolation
(`${VAR}`) inside the file is **not** supported in v1 (it invites surprising indirection and
secret expansion); the file holds literal paths.

**Security-relevant keys and a differing environment (P2-3).** For `server.listen`,
`server.read_only`, `server.public_origin`, `server.principal`, `server.tls.*`, `acl.*`, `token.*`,
`limits.token_max_lifetime`, `dashboard.*`, `permission.*` and `writer.lock_dir`, an environment
value that **differs** from a present file value is a **startup refusal** (the same fail-closed rule
as the new/legacy conflict), naming the key and the two sources, never a value; an equal value is
accepted with one warning. A stale `BRIDGE_READ_ONLY=0` or an old `BRIDGE_ALLOWED_ROOTS` can therefore
not silently override a hardened file. Lists **replace**, never merge (a file list is the complete
list; an env list replaces it, and a differing env list is refused as above).

**Empty values (the ADR-026 §2.3 interaction).** ADR-026 §2.3: an empty `CIRAK_X`/`BRIDGE_X` counts
as unset for the **environment** compatibility rule. In the **file**, missing = default and `""` is
an explicit unset; an empty environment variable does not override a file value (it is unset) and a
`""` file value is an explicit unset. The one table:

| Env | File | Result |
| --- | --- | --- |
| unset or empty | missing | default |
| unset or empty | `""` | explicit unset (no default) |
| unset or empty | value | the file value |
| value | missing/`""` | the env value (one source) |
| value | value, equal | the value, one warning |
| value | value, different | **refusal** (security key) or the env value (non-security key; stated here) |

Non-security keys keep the plain flag/env/file/default order (env wins); only the security-relevant
keys above refuse a differing env/file pair. Tests cover both.

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
| `harness.opencode.url` | string | `OPENCODE_URL` (unchanged, not legacy) | `http://127.0.0.1:4096` | no |
| `harness.opencode.username` | string | `OPENCODE_SERVER_USERNAME` (unchanged) | `""` | no |
| `harness.opencode.password_file` | string | `OPENCODE_SERVER_PASSWORD_FILE` (unchanged; `readSecret` also accepts the inline `OPENCODE_SERVER_PASSWORD` in the window) | `""` | **yes (file)** |
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
`dashboard.addr` are mutually exclusive; a TLS cert/key pair is all-or-nothing; `token.file`,
`token.credential` and an inline `BRIDGE_TOKEN` are mutually exclusive; lifecycle/overlay and Claude
enablement keep their all-or-nothing rules. **ACL IDs are the bare native IDs**: OpenCode
`ses_…`, Claude `claudecode:<uuid>` (the policy compares the bare ID, `internal/mcp/server.go:102`;
the README uses `BRIDGE_ALLOWED_SESSIONS=ses_EXISTING_NATIVE_ID`). An `opencode:` prefix is not an
ACL ID (P3-2).

Example (the wizard writes this shape; comments are not JSON, they annotate the ADR only; the local
`token.expires` is the D30 7-day **rotation deadline**; local mode has no runtime expiry, ADR-019, and
the S1 trigger keeps it local):

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
          "sessions": ["ses_example"]},
  "writer": {"lock_dir": "/var/lib/cirak/writer-locks",
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

- A secret is only a **file reference** or a **systemd credential name**. The **token and OpenCode
  password files keep today's `readSecret` rule** (P3-3): the `_FILE` path is absolute; the file is a
  private **regular** file, mode `& 0077 == 0` (so `0400` is accepted), no symlink, 1–16384 bytes, and
  no CR/LF/NUL in the value; there is **no owner check** beyond the process being able to read it
  (`cmd/bridge/secret.go:13-43`). The ADR does not tighten this rule. `init` creates new files `0600`.
  A credential is resolved from `$CREDENTIALS_DIRECTORY/<name>` only when systemd set it.
- The config file itself: absolute canonical path, no symlink in any component, regular file, owned
  by the process UID (or root when the process runs as root), mode exactly `0600`; the parent
  directory not group/world-writable. `cirak config check` refuses otherwise, before any listener.
- A **password typed after a 401** goes to a `0600` `harness.opencode.password_file`; it is never
  written to the config, argv, shell history or the journal (P3-4). The token is printed at most
  **once in the whole `init` run** (a single TTY print; `--yes` prints the path only).
- New files (token, config, backup) are created with `O_EXCL|O_NOFOLLOW`, the mode set at creation,
  and a parent directory that is not group/world-writable (P3-4). A `--force` replacement is written to
  a fresh `O_EXCL` temporary file in the same directory and renamed over the old path after the backup;
  the old file is never opened for truncation.
- `CIRAK_TOKEN` (inline) is never written by the wizard and is never a config key; `init`/`import`
  refuse to persist an inline secret and print the `install -m 0600` command instead (no value in
  the output).
- Strict unknown/duplicate-key rejection and no env interpolation; a hostile file can only fail
  closed.

### 1.5 `cirak config check`

Static validation, no network, no listener: parse and strict-check; every field's existing validator
(reusing the runtime validators, not a copy); config/token/password/TLS file rules (the config file's
owner/mode/parent check; the token/password file's today's `readSecret` rule); token length and
expiry (against the injected clock); root ownership `root` or the service user, canonical path, and
the service user's `r-x` via `stat`/ACL (the optional `--as <user>` read-only probe, D30 Q8);
lifecycle file rules; overlay directory rules; dashboard socket parent; Claude files/versions shape;
unknown `CIRAK_*`/`BRIDGE_*` environment names as warnings (names only); the security-key env/file
conflict check of §1.2. Output is a redacted summary (each secret `set`/`unset`), exit `0` clean, `2`
refused, `1` warnings. This is the command an operator runs before `systemctl restart` and in CI.

## 2. `cirak init`

Interactive by default (TTY), with a non-interactive `--yes` path whose non-secret answers are flags
(`--listen`, `--root` (repeatable), `--opencode-url`, `--service-user`, `--config`, `--token-file`,
`--expires`, `--remote`/`--local`, `--tls-cert`/`--tls-key`, `--install-service`, `--no-service`,
`--force`). `--yes` never prompts; missing non-secret input is an error; a token is generated only
if `--token-file` names a new file.

1. **Detect OpenCode.** Probe the configured URL with the same bounded reads the Bridge uses
   (`GET /session/status`, credentials from the prompt/file); on 200 record reachability and, best
   effort, the version (the exact version endpoint is UNVERIFIED; otherwise print `unknown`; D30 Q7
   accepts `unknown`); on 401 ask for credentials (echo off) and retry; the typed password goes to
   the `0600` `password_file` (§1.4), never argv/history/config; on connection failure print the URL
   and the `systemctl --user status opencode` hint. Nothing is installed or changed.
2. **Ask for workspace roots** (repeatable) and **check the live ownership/permission rules**:
   - the root must exist, be canonical, and be owned by `root` or the service user
     (`internal/lifecycle/config.go:344-345`; F-H5-1);
   - the service user needs `r-x` on the root and on the created directories, and read on the `.git`
     it will use (`internal/security/lifecycle_root.go`; F-H5-2) — checked with `stat`/ACL and,
     optionally, `sudo -u <service-user> test -r/-x` (the read-only probe; D30 Q8; the operator sees
     the probe);
   - on failure print the **exact** fix, e.g.
     `chown root:<group> <root> && chmod 0710 <root> && setfacl -m u:<service-user>:rwx <root>`
     (and the `install -d -o … -g … -m …` for a create target). **`init` never applies it; it only
     prints it** (P3-5), and the wizard asks before printing a command that needs root.
3. **Generate a token** (default 64 chars, ≥43; file mode `0600`; expiry default `+7 days` for both
   local and remote; the wizard writes `limits.token_max_lifetime: "168h"` explicitly so the pair is
   consistent; `--expires` must stay within it). The value is printed **once in the whole run, only
   on a TTY**, with a warning, or its path is printed in `--yes` mode; never in the config, a log, or
   an error. **The local mode keeps the 7-day value (D30 Q5) because S1 changes the remote-mode
   trigger (P2-1):** a token expiry no longer switches to remote; remote needs a public
   origin/principal or TLS. The value is a **rotation deadline** in the local config, not a runtime
   expiry: local mode has no expiry (ADR-019), and `doctor` warns when the recorded deadline passes.
   An env-only `TOKEN_EXPIRES` without remote settings now starts local with a startup warning
   instead of today's refusal; that is the one behaviour change needed to keep D30's local 7-day
   default, and the remote boundary is unchanged. So `init --local` passes `config check`, and the S1
   test "an `init --local` config passes `config check`" and the ingress test "`TOKEN_EXPIRES` alone
   stays local" pin it.
4. **Write the config** (§1.3) mode `0600`. **Ownership (P2-4):** run `init` **as the service user**
   (`sudo -u cirak cirak init`) so the files are `cirak`-owned; when the invoking uid is not the
   service uid, `init` prints the exact `install -o <user> -g <group> -m 0600 <src> <dst>` commands
   (and the `install -d` for the directories) and **writes nothing it would then refuse** — it never
   applies `chown`/`chmod`/`setfacl`. The system directory must already exist and be writable by the
   invoking uid; otherwise `init` prints `install -d -o <user> -g <group> -m 0700 /etc/cirak` and
   stops before writing. With `--service-user` empty (local mode) the files are the operator's. The
   lifecycle JSON (mode `0600`) follows. `--force` is required to replace an existing config; a backup
   is made (`config.json.bak-<timestamp>`, mode `0600`).
5. **Optionally install a service** (only with `--install-service`, and root is required and stated
   up front; never implied):
   - **Linux/systemd:** unit `cirak.service` with the hardening: **`LoadCredential=`,
     `RuntimeDirectory=` and `StateDirectory=` are proven live (F-H5-3); the other hardening keys are
     proposed** (P2-2). `User=`/`Group=`, `ExecStart=/usr/local/bin/cirak serve`,
     `Environment=CIRAK_CONFIG=/etc/cirak/config.json`, `StateDirectory=cirak cirak/writer-locks
     cirak/overlay` + `StateDirectoryMode=0700` (the writer lock is persistent, never `/run`, P3-6),
     `RuntimeDirectory=cirak-admin cirak-dashboard` + `RuntimeDirectoryMode=0700`,
     `LoadCredential=mcp-token:/etc/cirak/token` (config `token.credential: "mcp-token"`),
     `ProtectSystem=strict`, `PrivateTmp=yes`, `RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6`,
     and `NoNewPrivileges=yes` **only when the Claude adapter is off**. With the adapter on, the
     child runs under `sudo -u hbclaude` (`docs/deploy/CLAUDE_CODE_RUNBOOK.md:20`); `sudo` is setuid
     and `NoNewPrivileges=yes` would break every run, so the unit **omits `NoNewPrivileges`** and
     uses `ProtectHome=off` (or `ReadWritePaths=<claude home>`); the wizard says which it chose. The
     unit is written, not started, unless the operator confirms; `systemctl daemon-reload` and the
     restart hint are printed.
   - **macOS/launchd:** a `LaunchAgent` (`~/Library/LaunchAgents/dev.cirak.agent.plist`) for the
     user (D30 Q6: LaunchAgent by default), or a `LaunchDaemon` (`/Library/LaunchDaemons`) only when
     the operator asks and root is available; `KeepAlive`, `RunAtLoad`,
     `StandardOutPath`/`StandardErrorPath` under `~/Library/Logs/cirak` (or `/var/log/cirak`),
     `EnvironmentVariables` with `CIRAK_CONFIG`; no `LoadCredential` equivalent, so the token is a
     `0600` file owned by the run user.
   - It **never needs root** unless installing a *system* unit/daemon; a user-level setup runs
     entirely as the operator. The wizard says which case applies before asking for anything.
   - Token source follows the chosen install: with the systemd unit, the config keeps
     `token.credential: "mcp-token"` (and `token.file: ""`) so `LoadCredential` is the only source
     (D30 Q5: systemd credential by default); without a unit (or on macOS), it keeps `token.file`
     with the `0600` file. The two are mutually exclusive, and `config check` refuses both set.
6. **Print the Claude connector values**: the listener URL (or the remote origin), the header name
   `Authorization`, and the token **only if it has not already been printed in this run** (at most one
   TTY print in the whole run) with `set +o history` and a warning; in `--yes` mode only the token file
   path. Then print the smoke steps: `cirak config check` → start →
   `cirak doctor --smoke` (a bounded `initialize` + `tools/list` against the **already-running**
   local MCP endpoint; D30 Q9; `tools/list` must show the expected surface, e.g. 11 tools with the
   relay off).

`cirak init` does not start the long-running service by default; it writes and prints the one
command to start it. Re-running it on an existing config asks before changing anything.

## 3. `cirak doctor`

Read-only diagnostics, safe to paste, no secret values (each is `set`/`unset` or an expiry timestamp):

1. **Config** — path, source of each effective setting (flag/env/file/default), strict-check result,
   file mode/owner, token/credential presence and expiry (no value), TLS cert/key presence and
   expiry (no content); the **config path in use is always logged/printed** (P3-7).
2. **OpenCode** — reachability, the same status/history reads the Bridge makes, any 401/403 as a
   credential problem, version if available (`unknown` acceptable, D30 Q7).
3. **Sockets** — admin/dashboard paths, parent modes, `srw-------`/`drwx------`, stale-socket note;
   listener bound/loopback check; remote/TLS consistency.
4. **Lifecycle/overlay** — lifecycle file rules, each root's owner, mode and the service user's
   `r-x`, `.git` readability, overlay directory mode/`StateDirectory`/writability (F-H5-1..3).
5. **ACL/Claude** — allowed roots/sessions shape and overlap (bare IDs, P3-2), `claudecode:`
   enablement all-or-none, Claude version file, wrapper path/mode (without reading the wrapper
   content).
6. **Environment** — a list of `CIRAK_*`/`BRIDGE_*` variables in effect (names only; the
   `OPENCODE_*` names are listed as unchanged, not legacy), unknown names as warnings, the
   new/legacy conflict check, and the security-key env/file conflicts (§1.2). The **unit** source is
   available with `doctor --from-unit <name>` (P2-5): the effective environment from
   `systemctl show -p Environment -p EnvironmentFiles` and the named files, value-free; it is the
   same source the import uses.
7. Optional `--smoke`: the bounded local MCP handshake above against an **already-running** Bridge
   (a client call: `initialize` + `tools/list`, one prompt-free request); never a prompt, never a
   mutation (D30 Q9).

Exit `0` all checks pass, `1` warnings, `2` a check failed. It never starts a listener, never writes,
never restarts anything, and never includes a token, a password or a free-text provider error.

## 4. Local-only and remote modes

- **Local (default):** `server.listen 127.0.0.1:8080`, no TLS, dashboard on a unix socket, token
  still required (the connector is local but a token keeps the surface closed), admin socket under
  `/run/cirak-admin`, writer lock under the persistent `/var/lib/cirak/writer-locks`, overlay under
  `StateDirectory=cirak`. The config **records the 7-day rotation deadline** (D30 Q5) in
  `token.expires`, but local mode has **no runtime expiry** (ADR-019); `doctor` warns when the
  deadline passes. The remote-mode trigger is a public origin/principal or TLS, **not** the token
  expiry (the S1 change, P2-1), so an `init --local` config passes `config check`.
- **Remote:** TLS cert/key (or a reverse proxy that terminates TLS and forwards to loopback),
  `public_origin` and `principal`, a token with an explicit expiry, and the connector URL from the
  public origin. `init --remote` asks for the cert/key and the origin, warns that a TLS cert and key
  are both required (never a plaintext fallback), and points at the existing Caddy/Traefik guidance
  (`docs/deploy/Caddyfile.example`, `docs/deploy/TRAEFIK_PRIVATE_BACKEND.md`). It never opens a
  firewall or edits a proxy; those stay operator steps.

## 5. Migration from the current env/drop-in deployment

The live H5 host must keep working with no edits. ADR-026 (accepted) keeps `BRIDGE_*` accepted for
one minor release, so the migration is opt-in and reversible:

1. Run the new binary **read-only**: `cirak doctor --from-unit <name>` (or with the unit's
   `EnvironmentFile`) reports the **unit's** effective environment and the five live checks without
   starting anything; `cirak doctor` with no source is the shell-only first look and must not be used
   for the comparison (P2-5).
2. **Import the unit's environment, not the shell (P2-5):** `cirak config import --from-unit <name>
   --out /etc/cirak/config.json` (mode `0600`) reads the service's effective environment from
   `systemctl show -p Environment -p EnvironmentFiles` and the named files, **value-free in every
   output**; `--env-file <path>` is the explicit alternative. It writes the non-secret settings as
   §1.3; a legacy inline `BRIDGE_TOKEN`/`OPENCODE_SERVER_PASSWORD` is **not** written — the command
   prints the `install -m 0600` path to move it to a file/credential and refuses to inline it.
   `--reject-unknown` makes any unrecognised `CIRAK_*`/`BRIDGE_*` name an error.
3. `cirak config check --config /etc/cirak/config.json`, then `cirak doctor --config … --from-unit
   <name>` and compare the effective settings with the unit's environment (the doctor prints both
   side by side, secrets as `set`/`unset`). The comparison uses the **unit's** environment, not the
   operator's shell.
4. Add `Environment=CIRAK_CONFIG=/etc/cirak/config.json` (or `EnvironmentFile=`) to the unit, keep the
   legacy drop-in for one restart, and confirm `tools/list` and the smoke check are unchanged. The
   start logs the config path in use (P3-7). Because the imported file and the unit env are equal,
   startup accepts them with one warning per variable; any **difference** is a refusal (P2-3), which is
   the intended guard. **Remove the legacy lines only when the diff of step 3
   is empty**; a non-empty diff is a refusal to proceed (P2-5).
5. After the compatibility window, remove the migrated `BRIDGE_*` lines; the config file is now the
   source. **Rollback:** keep the drop-in backup (`zzzz-*.conf.bak-<timestamp>`); to roll back, remove
   `CIRAK_CONFIG` (or the `EnvironmentFile` line), restore the backup drop-in, `daemon-reload`,
   restart; the legacy env still works during the window. A removal after the window needs the
   config file and the drop-in backup.

**Open limitation (not a decision):** the implicit `$XDG_CONFIG_HOME/cirak/config.json` discovery can
let a per-user file configure a service run as that user. The ADR keeps D30's discovery paths; a
future decision may narrow the **service** run to `/etc/cirak` only. Every start logs the path in use
so the operator sees which file won.

## 6. Test plan (RED-first, no live services)

Unit tests with a fake FS/clock/exec and golden files; nothing starts a real listener or service.

1. **Format/strictness:** unknown key refused; duplicate key refused; wrong type; trailing document;
   >1 MiB; `token.value`/`password` refused; comments are invalid JSON (documented).
2. **Precedence:** flag > env > file > default per setting; new/legacy env with different values
   refused; equal values accepted with exactly one warning per variable; no implicit cwd file;
   `--config` missing is an error; no file = today's env-only behavior; the security-key env/file
   **differing** pair refused and equal warned (§1.2); list replace (not merge); the empty table.
3. **Secrets:** config/token modes and owners refused when wrong (the config file's stricter rule; the
   token/password's today's `readSecret` rule, including `0400` and no owner check); a symlinked
   config/token refused; inline secrets impossible; credential resolution only from
   `$CREDENTIALS_DIRECTORY`; no secret in `config check`/`doctor` output (grep the golden output for
   a canary token); the typed password goes to `password_file`; the token prints at most once; the
   `O_EXCL|O_NOFOLLOW`/mode/parent checks.
4. **Schema validators:** every existing rule (mutual exclusions, TLS pair, relay/incarnation enum,
   lifecycle/overlay all-or-none, Claude all-or-none, redact bounds) is reused, not reimplemented;
   a config that the env can express is accepted, an invalid one is refused with the same class of
   error; the ACL example uses the bare native ID.
5. **Init:** token generation length/mode/expiry; no chown/setfacl/chmod is applied (a fake exec
   records zero privileged commands); the exact fix command is printed; the invoking-uid ≠
   service-uid case prints the `install -o/-m` commands and writes nothing it would refuse; a
   missing/unwritable system directory prints `install -d` and stops; `--yes` without answers fails;
   an existing config needs `--force`; a backup is made; the printer shows the token only on a TTY,
   at most once in the run, and never in evidence; the rendered unit contains the hardening and no
   secret; **with the Claude adapter on it has no `NoNewPrivileges=yes` and the chosen ProtectHome
   form**; **the writer lock is under `StateDirectory`**; **`init --local` passes `config check`**
   (with the 7-day recorded deadline), and the ingress test "`TOKEN_EXPIRES` alone stays local".
6. **Doctor:** each check's pass/fail with fakes; redaction (no token/password/free text); exit
   codes; unknown env names warned; the config path printed; `doctor --from-unit` uses the unit's
   environment; a **local** token whose recorded deadline has passed is warned (not a runtime
   expiry); `--smoke` performs one bounded `initialize`+`tools/list` and nothing mutating, only
   against a running service.
7. **Migration:** env-only vs imported config produce the same effective settings (a table-driven
   equivalence test using a fake unit environment containing `READ_ONLY=1`); inline secrets are
   refused/preserved as file references; a non-empty diff blocks the legacy-line removal; rollback
   with the drop-in backup works in the test.
8. **Compatibility:** the H5 drop-in's legacy `BRIDGE_*` env still starts the binary with no config
   (the golden test is the current effective defaults), and the renamed `CIRAK_*` wins when both are
   equal; the `OPENCODE_*` names are not legacy and produce no deprecation warning.

Mutations (must be killed): precedence swapped; conflict accepted; unknown key ignored; duplicate key
accepted; trailing data or >1 MiB accepted; a list merged instead of replaced; an implicit cwd file
loaded; secret persisted inline; config mode unchecked; the token/password `_FILE` rule tightened by
rejecting `0400` or adding an owner check (P3-3 keeps today's rule); a symlinked config/token
accepted; a credential resolved outside `$CREDENTIALS_DIRECTORY`; a runtime validator reimplemented
with a divergence; an `opencode:`-prefixed ACL ID accepted; init applies `chown`/`chmod`/`setfacl`
silently; the invoking-uid mismatch writes files the service cannot read; the system directory created
by the wrong user; an existing config overwritten without `--force`; a backup not made; token printed
twice or in `--yes`/to a log; the
typed password written to config/argv/history; the rendered unit contains a secret; unit drops
`StateDirectory`/`LoadCredential`; unit keeps `NoNewPrivileges=yes` with the adapter on; the writer
lock under `/run`; `init --local` refused by `config check`; `TOKEN_EXPIRES` switches to remote; the
security env/file conflict accepted; the import read from the operator's shell instead of the unit;
legacy lines removed on a non-empty diff; doctor prints a value; the config path not logged;
`OPENCODE_*` warned as legacy; `--smoke` sends a prompt or starts a listener; migration writes the
legacy inline token.

## 7. Slices

**ADR-026 S1 (the code rename) is a prerequisite:** the `CIRAK_*` surface and `cmd/cirak` land there;
the config keys are name-neutral. D30 Q10: the `CIRAK_*` surface is used from S1 below (after the
ADR-026 rename slice), not deferred to a separate config-rename slice.

1. **S1 — config package and `config check`:** JSON schema, strict parsing, precedence (flag/env/file/
   default) with the ADR-026 compatibility warning/refusal, the §1.2 security-key env/file rule, the
   secret file rules, the ACL IDs, reuse of the runtime validators, the **remote-mode trigger change
   (`token.expires` alone no longer implies remote; local mode records the deadline but keeps no
   runtime expiry, ADR-019)**. Docs + CONTRACTS note. No wizard.
2. **S2 — `cirak init` core:** detection, root/ACL checks and exact fix output, token generation
   (64 chars, 7 days), config + lifecycle write, ownership flow (run as the service user or print
   `install -o/-m`), connector values and the smoke instructions; no service install yet.
3. **S3 — service install and `doctor`:** systemd unit (the proven/proposed split; no
   `NoNewPrivileges` with the adapter on; StateDirectory writer lock) and launchd plist
   rendering/install; `doctor` static + live checks; `--smoke`; `doctor --from-unit`.
4. **S4 — migration and remote mode:** `config import --from-unit`/`--env-file`, the side-by-side
   doctor with the unit's environment, remote/TLS wizard path, runbook updates; the H5 host migration
   is an operator step, never run by a worker.
5. **S5 — no separate config rename slice:** the ADR-026 S1 rename is the prerequisite; the config
   schema keys are name-neutral; this ADR's S1-S4 ship after it.

Each slice is one PR, RED-first, with the cases and mutations it owns; no slice needs a live service
for acceptance.

## 8. Questions for the maintainer: decided (D30)

The maintainer answered every question below (D30, 2026-11-21, #117). Each question keeps its text;
the **Decided (D30)** line is the decision.

1. **Format:** JSON stdlib (recommended) or accept one TOML dependency for comments/ergonomics?
   **Decided (D30):** **JSON, stdlib only.**
2. **Config location:** `/etc/cirak/config.json` + `$XDG_CONFIG_HOME/cirak/config.json`, or a single
   `/etc/cirak/config`? Is a repository-local config ever allowed (recommended: only with `--config`)?
   **Decided (D30):** `/etc/cirak/config.json` + `$XDG_CONFIG_HOME/cirak/config.json`; a
   repository-local config **only** with `--config`.
3. **Lifecycle roots:** keep the separate lifecycle JSON referenced by `lifecycle.config_file`
   (recommended, no contract change), or embed `roots` in the main file and deprecate the separate
   file?
   **Decided (D30):** keep the **separate lifecycle file** (`lifecycle.config_file`).
4. **Service user:** create a dedicated `cirak` system user (packages) or run as the operator's user
   (local-first)? This decides the unit's `User=` and the token/root ownership the wizard checks.
   **Decided (D30):** the **packages create a `cirak` system user**; **local mode runs as the
   operator**.
5. **Token defaults:** 24h or 7 days, and is 64 chars the default? The H5 sitting used an in-place
   rotation with `LoadCredential`; should `init` default to a credential rather than a file?
   **Decided (D30):** **64 characters, 7 days, a systemd credential by default** (with the unit).
6. **macOS default:** LaunchAgent (user, no root) vs LaunchDaemon (system) — recommended Agent unless
   asked.
   **Decided (D30):** **LaunchAgent by default**; a LaunchDaemon only when the operator asks.
7. **OpenCode version detection:** is there a supported version endpoint, or is "unknown" acceptable
   in `init`/`doctor`? (UNVERIFIED; the probe uses the Bridge's existing session/status reads.)
   **Decided (D30):** **"unknown" is acceptable**; the version endpoint stays UNVERIFIED.
8. **Root permission check:** is an optional `sudo -u <service-user> test -r/-x` acceptable in the
   wizard (it is a read-only host probe) or must the check stay `stat`/ACL-only?
   **Decided (D30):** the **read-only `sudo -u <service-user> test -r/-x` probe is allowed**.
9. **`--smoke`:** is a temporary local MCP `initialize`+`tools/list` against a not-yet-started service
   acceptable, or should it only run against an already-running service (a client call, no listener)?
   **Decided (D30):** `--smoke` runs **only against an already-running service**.
10. **Rename timing:** ship S1–S4 under the current binary name with `BRIDGE_*` only and add
    `CIRAK_*` in S5, or wait for ADR-026 and ship the `CIRAK_*` surface from S1?
    **Decided (D30):** the **`CIRAK_*` surface from S1**, after the ADR-026 rename slice; there is no
    separate config-rename slice.

## Out of scope

- Any runtime authority, guard, audit or ADR-010/011/012/024/025 change. The single exception is the
  S1 remote-mode trigger (`token.expires` alone no longer implies remote; P2-1); it changes no
  authority.
- A package manager or installer beyond ADR-026's distribution (this ADR is the config/wizard
  surface only).
- Editing the live H5 host, systemd, `/etc`, the connector or a proxy; every migration step is an
  operator action.
- A web UI for setup; `cirak init` and `doctor` are terminal commands.
