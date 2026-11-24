# Read-only dashboard: operator setup (ADR-023)

The dashboard is a **local, read-only** view of the sessions the Bridge already authorizes: the
session list and detail, native diffs, and the audit ring. It is **off by default**. It has no
write route and cannot send a prompt, interrupt or release anything. Status: implemented and
tested against fakes; the live acceptance (R-DASH) is **not yet exercised**, and the native diff
response shapes are unverified against a live server.

## Enable it (unix socket, recommended)

1. Let systemd create a private directory: add `RuntimeDirectory=harness-bridge-dashboard` and
   `RuntimeDirectoryMode=0700` to the Bridge unit (owned by the unit's `User=`, mode exactly `0700`,
   removed when the unit stops). If you create it by hand instead, it must be a real directory
   owned by the service user with mode exactly `0700` (not a symlink).

2. Set one variable in the Bridge's environment (never both):

   ```text
   BRIDGE_DASHBOARD_SOCKET=/run/harness-bridge-dashboard/dash.sock
   ```

3. Restart the Bridge. A bad configuration (a directory that is not `0700`, a non-loopback address,
   both variables set) stops the start. With neither variable set there is no listener, no
   socket and no goroutine.

The alternative `BRIDGE_DASHBOARD_ADDR=127.0.0.1:8765` (host exactly `127.0.0.1` or `::1`) serves
TCP on the loopback interface only; requests must carry a `Host` of `localhost`, `127.0.0.1` or
`[::1]`, and an `Origin` header from any other host is refused. The unix socket skips the Host
allowlist (its `0700` directory is the fence and no browser can reach it), but it refuses **any**
request that carries an `Origin` header with 403: there is no browser origin for the socket.

## Reach it from your workstation (SSH forwarding, D17 Q1)

The host is a single-operator machine, so the dashboard has no token. Forward the socket over SSH
instead of exposing a port (OpenSSH 6.7 or newer forwards a unix socket):

```text
ssh -N -L 127.0.0.1:8765:/run/harness-bridge-dashboard/dash.sock <operator>@<bridge-host>
```

Then open `http://127.0.0.1:8765/` on the workstation. For the TCP transport forward the port
instead: `ssh -N -L 8765:127.0.0.1:8765 <operator>@<bridge-host>`. The forward must stay bound to
the workstation's loopback; never bind it to `0.0.0.0`.

## Redaction

A built-in list (`sk-ant-`, `ghp_`, `github_pat_`, `AKIA...`, private-key blocks, bearer tokens) is
always on for diffs and the audit ring. To add your own patterns set (this variable name is a
**slice-5a proposal**, not an ADR decision, and may be renamed):

```text
BRIDGE_DASHBOARD_REDACT_EXTRA=["corp-[0-9a-f]{16}"]
```

A JSON array of at most 16 RE2 patterns of 1 to 256 bytes. An invalid value stops the start and is
not logged. Redaction is a convenience: a secret that matches no pattern is still displayed to
whoever can open the dashboard.

## What you see

- **Sessions:** exactly the sessions in `BRIDGE_ALLOWED_SESSIONS` that the Bridge can read (the same
  set as `list_sessions`), with their state, guard and current execution. A `claudecode:` entry is a
  pending row (no history or diff until ADR-021's dashboard interface exists).
- **Lifecycle root:** when the lifecycle catalog is configured (ADR-022), a session's directory is
  labelled with the lifecycle root that contains it. This is a label; it never adds a session.
  Nothing in the production Bridge wires that configuration yet, so the column shows `-`.
- **Archived sessions** are hidden; add `?archived=1` to the URL (or use the link on the list) to
  show them. Nothing is archived yet because the archive overlay is not persisted.
- **Filters and refresh (ADR-023 A1, S1):** with script, the search box and the state and harness filters
  appear and keep their value in the address (`?q=`, `?state=`, `?harness=`), and the page polls every 5 s
  while the tab is visible and not at all while it is hidden. Without script the filters are not shown,
  the list is complete and the Refresh link reloads it. The page is dark; the light theme comes with a later slice.
- **Audit:** `/audit` shows the last 1000 audit lines the Bridge wrote, with free text removed.

## Limits

Four data requests and eight requests at a time; a request is cut at 5 seconds; no queue. A source
that fails answers `503` with `Retry-After`. The page is read-only by construction, and the Bridge
never runs `git` for the dashboard: the working-tree diff is the native `/vcs/diff`.
