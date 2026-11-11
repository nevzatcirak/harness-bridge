# ADR-023: read-only session dashboard

Date: 2026-11-11. Status: **PROPOSED**. Design only; no production code in this
change.

**Maintainer decision D9 (2026-11-11, via Planning Desk):** a separate
**dashboard UI**, "like the Claude Code coder view", that shows which sessions
exist and are running, each session's path (cwd/root), the run state and current
execution, the **diff of the changes** each session made, and something the
maintainer can follow over time. It is **read-only**: all actions stay in Claude
Chat through the existing MCP tools.

Numbering: ADR-021 (Claude Code adapter, issue #152) and ADR-022 (session
lifecycle, issue #153) are reserved by parallel tasks; ADR-023 is this document.
ADR-021 is a **dependency**: its Bridge-owned history store is the only Claude
Code data source this dashboard may use (§1).

This ADR must preserve ADR-010 (single writer, in-memory execution records),
ADR-011 (late completion), ADR-012 (repository-aware session registry and the
operator session ACL), ADR-014 (workspace evidence and admission), ADR-017
(liveness), ADR-018 (permission rejection, report-only) and ADR-019 (connector
token lifetime). It amends none of them.

**Non-goal note.** `HARNESS_BRIDGE_SPEC.md:34` says not to introduce a "browser
frontend" without a demonstrated requirement. D9 is that demonstration. The
dashboard stays an operator-facing, local-only read layer; it does not replace
the Claude Chat connector and adds no MCP tool.

## Problem

The Bridge exposes session state, executions and results only through MCP calls
from Claude Chat. Following several sessions over time from the chat window is
awkward: the operator cannot see a live list, the current execution per session,
or what each session changed without asking Claude Chat, and every answer is a
snapshot of one call. The native tools have views (OpenCode TUI; the Claude Code
coder view), but neither spans both harnesses nor the Bridge's own execution and
audit state.

The dashboard must answer, without mutating anything:

1. Which sessions exist, under which harness, in which root, and what state is
   each in?
2. Which execution is current for a session; is a guard held; is a native
   permission wait in progress?
3. What did the session change (diff), and how did that change over time?
4. What does the audit trail say happened?

## 1. Data sources

Everything below is **read-only**. No source is written, and no MCP tool call is
made for display.

### 1.1 Bridge state (authoritative for Bridge lifecycle)

| Data | Source | Notes |
| --- | --- | --- |
| Session IDs, harness, root, ACL | The existing read-only discovery path (`internal/mcp/discovery.go`, ADR-012 policy) plus the operator allowlist | Only ACL-authorized sessions are shown. An unavailable discovery result is shown as `unknown`; the dashboard never invents a session. Discovery is bounded like today's MCP discovery; a stall renders "discovery unavailable", never an empty list silently. |
| Executions (id, session, state, started/updated/ended, unresolved, permission rejection, guard-release time) | The execution manager's in-memory records (`internal/execution/manager.go:53-71`) | The dashboard needs a new **read-only enumeration** (`DashboardSnapshot`) that returns copies; today the manager exposes only per-ID `Snapshot`, `Get`, `SessionGuard` (`manager.go:492`, `:511`, `:532`). The new method takes `m.mu` and per-run `x.mu` briefly, in the documented order, and never takes `x.op`. |
| Current execution | The newest non-terminal execution for the session, by `Started` | Computed in the view model, not stored. |
| Guard | `Manager.SessionGuard(session)` (`manager.go:511-527`) | Shows `held` plus the state that holds it. |
| Audit | AUDIT-01 lines (`docs/CONTRACTS.md:188`), including the admin `guard_release` lines | The dashboard registers a **tee `slog.Handler`** into a bounded in-memory ring (1000 entries, drop-oldest) at startup. It never re-reads the journal, never opens the journal file, and never persists entries. The ring is display-only and is lost on restart, which is acceptable because the journal remains the audit authority. |

### 1.2 Native OpenCode (v1.19.4, `148842ef7cb1bc34192b3fff57d0f9721ab9c801`)

All paths below are in `anomalyco/opencode` at that exact commit, verified by
source reading only (OpenCode was not run).

| Data | Route | Source | Caveats |
| --- | --- | --- | --- |
| Session list | `GET /session` | `packages/opencode/src/server/routes/instance/httpapi/groups/session.ts:111` | Instance-scoped; workspace-bound sessions need the `directory`/`workspace` query fields (`middleware/workspace-routing.ts:22-25`). |
| Session detail | `GET /session/:sessionID` | `.../groups/session.ts:132` | Root/cwd from the session record. |
| Status map | `GET /session/status` | `.../groups/session.ts:121` | OpenCode **deletes** idle entries (`packages/opencode/src/session/status.ts:32,42-44`), so absence means idle; the dashboard must not report `running` from absence (same rule as the Bridge's ADR-017/018 logic). |
| Messages | `GET /session/:sessionID/message` | `.../groups/session.ts:179` | Needed to enumerate user-message IDs for diffs. |
| **Session diff** | `GET /session/:sessionID/diff?messageID=<id>` | `.../groups/session.ts:84` (path) and `:168-178` (endpoint); handler `.../handlers/session.ts:99-104`; query `DiffQuery` `:39-42` | **Per user message, not session-wide.** `SessionSummary.diff` returns `[]` when `messageID` is omitted (`packages/opencode/src/session/summary.ts:129-131`) and otherwise returns the diffs stored on that user message (`:135-141`). The diffs are computed at summarize time from `step-start`/`step-finish` snapshot hashes (`:82-100`) and stored on the user message (`:102-127`); if the snapshot feature is disabled (`config.snapshot === false`, `:115`) the result is always empty. Response items are `Snapshot.FileDiff`: optional `file`, optional `patch`, `additions`, `deletions`, optional `status` added/deleted/modified (`packages/schema/src/file-diff.ts:6-12`). |
| Working-tree diff | `GET /vcs/diff?mode=git\|branch&context=N` and `GET /vcs/diff/raw` | `.../groups/instance.ts:48-49` (paths), `:104-121` (endpoints); handler `.../handlers/instance.ts:51-59`; implementation `packages/opencode/src/project/vcs.ts:235,373-390` | Repo-level VCS diff (`mode=git` or `branch`), not attributable to a turn; 10 MB patch caps (`vcs.ts:12-13`). `GET /vcs/status` and `/vcs` help label it. |

**Never exposed:** `POST /vcs/apply` (`groups/instance.ts:50`), `/session/:id/abort`,
`/session/:id/revert`, prompt routes and any other mutating route. See §4.

**Snapshot storage and bounds.** OpenCode keeps snapshot data in a shadow git
directory under `Global.Path.data/snapshot/<projectID>/<hash(worktree)>`
(`packages/opencode/src/snapshot/index.ts:71`), pruned after 7 days
(`:23`), and excludes untracked files larger than 2 MiB (`:24,278-290`). The
dashboard therefore shows a per-turn diff only when OpenCode retained the
snapshot; otherwise it must say "diff unavailable" rather than "no changes".

### 1.3 Filesystem fallback (bounded, read-only)

If neither session diff nor `/vcs/diff` is available, the dashboard may run a
read-only `git diff` in the session root, as the Bridge's own service user, with:

- `GIT_OPTIONAL_LOCKS=0` so `git diff` never writes `.git/index.lock`;
- `--no-ext-diff --no-textconv --no-color` so repository configuration cannot
  run an external diff driver or textconv command;
- a clean environment (no `GIT_CONFIG_GLOBAL` surprises beyond what the root
  needs), `--no-pager`, a 5 s timeout, and at most 1 MiB of output;
- no hooks, no `git add`, no index refresh, no worktree changes.

The fallback is labelled as a working-tree diff (not per-turn). A repo whose
configuration refuses to cooperate is shown as "unavailable", never retried in
a loop.

### 1.4 Claude Code

From **ADR-021's history store** (PROPOSED, issue #152): the Bridge-owned
append-only record of `claude -p` stream-json events for runs the Bridge started
or resumed. The dashboard reads the file-edit tool events (`write`/`edit` tool
inputs and their recorded results/patch) from that store, read-only, through a
narrow interface that ADR-021 defines. Until ADR-021 is accepted and
implemented, Claude Code sessions show "diff unavailable (ADR-021 pending)";
this ADR does not define that schema and does not parse `~/.claude` JSONL.

Pre-Bridge turns are unavailable by construction (ADR-021, D7); the dashboard
shows ADR-021's explicit "history before first Bridge resume is unavailable"
marker.

### 1.5 Precedence and labelling

Per session, the dashboard shows the best available source and names it:

1. native per-turn session diff (OpenCode) → label "turn N, native snapshot";
2. ADR-021 file-edit events (Claude Code) → label "Bridge history";
3. `/vcs/diff` working tree → label "working tree (not per turn)";
4. bounded `git diff` fallback → label as such.

Sources are never silently merged. If two sources disagree, both rows are shown
with their labels; the dashboard never picks a winner without saying so.

## 2. Hosting and authentication

Assets exposed: repository content (diffs may contain secrets), session titles,
roots, audit lines. Security anchor: `HARNESS_BRIDGE_SPEC.md:125-133` (loopback
by default; never unauthenticated public; authorize users and sessions; never
log secrets; resist prompt injection). Diffs are **repository content**: the
dashboard is equivalent to read access to every displayed root.

### Option (a): separate loopback-only listener, reached by SSH tunnel

- Pros: no new public surface; no new credential, cookie, CSRF or session
  handling; matches "bind local endpoints to loopback"; the SSH tunnel is the
  authentication and transport, managed by the operator; MCP/connector code and
  ADR-019 token rotation are untouched.
- Cons: loopback TCP is **not** an authentication boundary on a multi-user
  host — any local user can connect; no remote access without a tunnel.
- Hardening: bind `127.0.0.1`/`::1` only and refuse `0.0.0.0`/`::` at startup;
  allowlist the `Host` header (`localhost`, `127.0.0.1`, `[::1]`); no CORS; no
  service worker; and on a multi-user host either run it on a single-operator
  host or use the unix-socket variant below.

### Option (a′): unix-domain socket in a 0700 directory (recommended shape of (a))

A dashboard listener is a `unix` socket inside a directory the service user owns
with mode `0700`, checked exactly as the ADR-020 admin socket does
(`internal/admin/socket_linux.go:17-55`). The filesystem mode is the auth fence;
there is no TCP port at all. Remote access uses OpenSSH unix-socket forwarding
(`ssh -L /tmp/dash.sock:/run/harness-bridge-dash/dashboard.sock`) or a local
`socat` proxy. This is strictly stronger than 127.0.0.1 and adds no credential.

### Option (b): on the public HTTPS listener behind its own dashboard auth

- Pros: reachable from any device without an SSH tunnel; one port.
- Cons: puts repository content behind a second credential and a second auth
  code path; the cookie must be rotated and protected (Secure, HttpOnly,
  SameSite=Strict) alongside the connector token (ADR-019); an auth bypass or
  XSS becomes remotely exploitable; CSRF risk is low because everything is GET,
  but session fixation and cache leakage are new concerns; it couples the
  dashboard's release cadence to the connector listener and its tests.
- Verdict: rejected for v1. A future remote need gets its own ADR with a token/
  cookie design, not a silent expansion of this one.

### Option (c): both

Sum of (b)'s risk and (a)'s operational work; not justified now.

### Recommendation

**Option (a), implemented as the unix-socket variant (a′)**; a 127.0.0.1 TCP
listener is allowed only on a single-operator host and with the Host allowlist,
and must never bind a non-loopback address. The dashboard is **disabled by
default** and enabled only by explicit configuration
(`BRIDGE_DASHBOARD_SOCKET` or `BRIDGE_DASHBOARD_ADDR`). No public listener, no
cookie, no new credential in v1. If the maintainer needs remote access from a
browser without SSH, that is a separate decision (b).

## 3. Rendering safety

- **Escaping.** Every native or repository-derived string (session title, root,
  message text, file path, diff hunk, audit values) is rendered through
  `html/template` in its context; no `template.HTML`, no `template.JS`, no
  `template.URL` for native data. Diffs render in a `<pre>` as text. Files and
  paths are never used to build a filesystem path in the handler.
- **CSP.** `default-src 'none'; script-src 'self'; style-src 'self';
  img-src 'self' data:; connect-src 'self'; frame-ancestors 'none';
  base-uri 'none'; form-action 'none'`, plus `X-Content-Type-Options: nosniff`,
  `Referrer-Policy: no-referrer`, `X-Frame-Options: DENY`.
- **No inline script.** One embedded `dashboard.js` and one `dashboard.css` from
  `embed.FS`; no CDN, no third-party code, no `eval`/`new Function`, no inline
  event handlers. The page works read-only if JS is disabled.
- **Size caps.** Session list ≤ 200 rows; messages/diff rows per page ≤ 100;
  per-file patch ≤ 256 KiB; total patch ≤ 1 MiB; response body ≤ 2 MiB. A
  truncated diff says "truncated at N bytes"; absence is never rendered as an
  empty diff.
- **Binary detection.** A NUL byte in the first 8 KiB, or invalid UTF-8 above a
  threshold, renders "binary file, not displayed" with size only. Images are not
  served; there is no download endpoint.
- **Optional redaction.** Best-effort patterns for known credential shapes
  (`sk-ant-`, `ghp_`, `github_pat_`, `AKIA[0-9A-Z]{16}`,
  `-----BEGIN … PRIVATE KEY-----`, `Authorization: Bearer …`) replace the value
  with `[redacted]` before rendering. It is a convenience, **not** a guarantee:
  an arbitrary secret in a diff is still displayed to whoever can open the
  dashboard, which is why the transport is local-only. Redaction hits are
  counted and displayed, never logged with content.
- **No logging of content.** Diff bodies, message text and file contents never
  enter slog, the audit ring or any cache file. Only counters and IDs are
  logged.

## 4. Live updates

**v1: polling, not SSE.**

- The page polls `GET /api/state?since=<etag>` every 2 s while visible and every
  10 s when hidden (Page Visibility API); the list view polls every 5 s.
- Responses carry `ETag`/`Last-Modified`; `304` is returned when the view model
  is unchanged. The view model is cached for 1 s server-side.
- Each request has a 5 s context bound; native reads reuse the existing bounded
  client patterns; a diff computation is single-flight per session and returns
  "unavailable" on timeout, never a partial diff presented as complete.
- Global concurrency: at most 4 concurrent dashboard requests and 8 in flight
  across the server; excess requests get `503` with `Retry-After`, not a queue.
- **No write endpoints at all.** The route table contains only `GET` (and
  `HEAD`). There are no forms, no POST/PUT/PATCH/DELETE routes, no methods on
  the data interfaces that mutate. This is structural: the dashboard depends on
  a read-only interface type, not on the manager/MCP server. A route-table test
  enumerates every registered route and fails if any method other than GET/HEAD
  exists; a second test sends each mutating method to every route and expects
  `405`; a third test asserts a dashboard session produces no AUDIT-01 mutation
  line and no change in the manager's mutation counters.

SSE is deferred: if polling proves too expensive, a later amendment can add one
SSE endpoint (`text/event-stream`, heartbeat 15 s, one stream per client,
no writes). It is not in v1 to keep the surface minimal.

## 5. Views

### 5.1 Session list (`GET /`)

```
┌ Harness Bridge dashboard ─────────────────────────────── read-only ─┐
│ Harness   Session                  Root                 State      │
│ opencode  ses_e9329c0b (title…)    /opt/…/workspace     idle       │
│ opencode  ses_1c6acec2 (title…)    /opt/…/proj-x        running    │
│ claude    claudecode:4e59c1bd      /opt/…/proj-x        completed  │
│ ─────────────────────────────────────────────────────────────────  │
│ last update 12:04:11Z · polling 2s · diff source: native snapshot   │
└─────────────────────────────────────────────────────────────────────┘
```

Columns: harness, session ID + title (truncated), root, state, guard, current
execution ID, last activity, changed-files count if a diff is available.
Filters are client-side only (no query-driven server work beyond pagination
limits).

### 5.2 Session detail (`GET /session/<harness>/<id>`)

```
┌ session ses_1c6acec2 · opencode ──────────────────── back ──────────┐
│ root /opt/…/proj-x · state running · guard held by exec 72a76b2b…   │
│ native session: busy · permission wait: none                         │
├ Executions ─────────────────────────────────────────────────────────┤
│ 12:17:09 72a76b2b… running   (current)                              │
│ 12:16:46 8065d4c0… completed                                        │
│ 12:16:24 a2e5f6a9… completed                                        │
├ Changes (turn 12:17:09, native snapshot) ───────────────────────────┤
│ h4/NOTE.md   modified  +1 −0                                        │
│ ┌─────────────────────────────────────────────────────────────────┐ │
│ │ +MARK-ee3f40                                                    │ │
│ └─────────────────────────────────────────────────────────────────┘ │
├ Audit (this session) ───────────────────────────────────────────────┤
│ 12:17:24 interrupt accepted exec=72a76b2b…                          │
└─────────────────────────────────────────────────────────────────────┘
```

Sections: header (root, state, guard, current execution, native status,
permission wait), executions timeline, per-turn diff with a turn selector,
working-tree diff labelled separately, and this session's audit slice. The
diff panel shows `unavailable` with the reason (snapshot disabled, binary,
truncated, ADR-021 pending) when it cannot show content.

### 5.3 Audit feed (`GET /audit`)

```
┌ Audit ─────────────────────────────────────────────── read-only ────┐
│ 12:17:24  prompt_submission  new       exec 72a76b2b…  operator     │
│ 12:17:22  prompt_submission  rejected  SESSION_BUSY    operator     │
│ 12:17:24  interrupt          accepted  exec 72a76b2b…  operator     │
│ 12:04:23  guard_release      refused   INVALID_INPUT   admin:uid=996│
└─────────────────────────────────────────────────────────────────────┘
```

Bounded ring (1000), newest first, filter by session/event client-side. No
free-text fields are rendered because AUDIT-01 stores none.

## 6. Threat model and tests

| # | Threat | Mitigation | Test |
| --- | --- | --- | --- |
| T1 | XSS from repository content, titles, diffs, audit values | `html/template` escaping everywhere; no inline script; CSP; no `template.HTML` | XSS fixture: titles `<script>alert(1)</script>`, paths `"><img src=x onerror=…>`, diff `</pre><script>`, message text; assert escaped output, no raw `<script`, CSP header present; parse HTML and assert no script node from data |
| T2 | Secret leakage through diffs | local-only transport (a′); redaction option; content never logged or cached to disk; size caps | fake `sk-ant-…` fixture: not logged, redacted when enabled, and never fetched from a public listener |
| T3 | Unauthorized access | unix socket 0700 / loopback only; refuse non-loopback bind; Host allowlist; no public listener; dashboard disabled by default | startup refuses `0.0.0.0:8080`; Host `evil.example` → 400; if (b) is ever enabled: no cookie → 401, bad/expired cookie → 401 |
| T4 | Cross-session access / path traversal | reuse the ADR-012 operator ACL; unknown or unlisted session → 404; IDs validated with the existing patterns; never build filesystem paths from native strings | request a session outside the ACL → 404; `../` in IDs → 400/404 |
| T5 | CSRF | no state-changing route exists; GET/HEAD only; no cookies in (a′) | route-table test + mutating-method sweep → 405 |
| T6 | Prompt injection through rendered content | the dashboard never feeds displayed content back to an agent; no LLM endpoint; no buttons that call MCP | code review + route table: no route accepts prompt input |
| T7 | Resource exhaustion | caps (§3), timeouts (5 s), single-flight diffs, 4 concurrent requests, 503 + Retry-After | oversized/binary/truncated fixtures; slow native fake hits the 5 s bound and renders "unavailable" |
| T8 | Clickjacking | `frame-ancestors 'none'`, `X-Frame-Options: DENY` | header test |
| T9 | DNS rebinding / CORS | Host allowlist; no CORS headers; no `Access-Control-Allow-Origin` | Host/Origin tests |
| T10 | Locking out or stalling writers | the dashboard's read-only enumeration takes `m.mu`/`x.mu` briefly, never `x.op`; native reads use their own bounded client, not the execution manager's slots | a fake holds a native read; a concurrent `send_prompt` still proceeds; race detector |
| T11 | Mutation by accident | read-only interface type; no mutating methods reachable from the dashboard package | mutating-method sweep + journal test: no `prompt_submission`/`interrupt`/`guard_release` line caused by dashboard traffic |

## 7. Implementation plan

Each slice is one PR, tests first (RED before GREEN where practical), with a
mutation check and an acceptance step. No slice changes the MCP tool contract.

| Slice | Scope | RED tests / mutations | Acceptance |
| --- | --- | --- | --- |
| 1 | `internal/dashboard` skeleton: config (`BRIDGE_DASHBOARD_SOCKET`/`ADDR`, disabled by default), listener validation (0700 unix socket or loopback only), route table, CSP/middleware, read-only `Source` interface | route-table test fails if any non-GET route exists; bind `0.0.0.0` refused; mutation: add a POST route → test RED | server starts on a 0700 socket, `curl` GET `/` returns HTML with CSP; TCP bind to 0.0.0.0 exits with an error |
| 2 | Sessions list + detail view model; read-only manager enumeration | XSS fixtures RED; mutation: remove escaping → fixture test RED; manager method copies, no aliasing | list matches MCP `list_sessions` for the ACL; detail shows state/guard/current execution |
| 3 | Diffs: OpenCode per-turn session diff (enumerate user messages), `/vcs/diff`, hardened `git diff` fallback; caps, binary, redaction; Claude Code hook behind a compile-time interface until ADR-021 | oversized/binary fixtures RED; mutation: drop `--no-ext-diff` → fixture with a hostile `diff.external` must not execute | diff matches `GET /session/:id/diff` for a known turn; fallback labelled; unavailable reasons shown |
| 4 | Audit ring (tee handler), polling JSON + ETag, single-flight, 503 | mutation: add a mutating method to the interface → compile/test RED; journal test | audit feed shows AUDIT-01 lines; no mutation lines from dashboard traffic |
| 5 | Hardening sweep (Host/Origin, timeouts, race), docs, live acceptance | full route sweep; race detector | live acceptance below |

### Live acceptance outline (R-DASH)

1. Start the Bridge with the dashboard **disabled**; confirm no socket/port.
2. Start it with `BRIDGE_DASHBOARD_SOCKET` in a 0700 directory; confirm the
   directory and socket modes, and that no TCP port is open.
3. Forward the unix socket over SSH (or use `socat`) and open the list; compare
   every row's session/root/state with `list_sessions`/`get_session` over MCP.
4. Send a prompt through MCP that writes a known file; within two polls the
   detail view shows the new turn and its diff; compare with the native
   `GET /session/:id/diff` response for the same user message.
5. Attempt `POST`, `PUT`, `PATCH`, `DELETE` on every route → 405; confirm the
   journal contains no `prompt_submission`/`interrupt`/`guard_release` line from
   the dashboard and the manager's counters are unchanged.
6. XSS fixture: create a session title and a file containing `<script>` and
   `onerror=`; confirm escaped rendering and the CSP header.
7. Restart the Bridge; confirm the dashboard keeps no state (no cache files) and
   the audit ring starts empty while the journal still has history.
8. Optional: fake `sk-ant-…` in a diff; confirm redaction and that nothing is
   logged.

Pass criteria: no mutating route exists; XSS cannot execute; caps hold; the
dashboard is unreachable without the local transport; no secret or diff body in
logs; Claude Code shows the documented "ADR-021 pending" state until that ADR
lands.

## Consequences

- Adds an operator-facing HTML surface, but only on a local transport, disabled
  by default, with no new credential.
- Requires one small read-only enumeration in the execution manager; it must not
  disturb the single-writer lock order.
- Depends on ADR-021 for Claude Code diffs; OpenCode diffs depend on snapshots
  being enabled and retained (7 days, 2 MiB per file).
- Adds no MCP tool, no database, no external service, no Node build chain.

## Open questions

1. **Transport.** Is the unix socket + SSH forwarding acceptable, or does the
   maintainer want 127.0.0.1 plus a small token? Is the host single-operator?
2. **Diff source default.** Should the detail view default to the per-turn
   native diff (requires enumerating user messages and snapshots) or the
   working-tree `/vcs/diff`? May the dashboard call the native diff per visible
   turn on every poll, or must it cache turn diffs for the poll interval?
3. **ADR-021 dependency.** The Claude Code history schema and retention are not
   settled; the file-edit event shape is UNVERIFIED until ADR-021 is accepted.
4. **Audit scope and retention.** 1000-entry ring OK? Include admin
   `guard_release` lines? Drop oldest or stop when full?
5. **Redaction.** On by default or opt-in, and who owns the pattern list?
6. **Session scope.** All ACL-authorized sessions, or only Bridge-created ones?
   Discovery cost on a large host.
7. **ADR-022.** Archived/closed sessions: shown, hidden, or a separate filter?
8. **Multi-harness display.** Is `opencode:`/`claudecode:` in the ID enough, or
   should harnesses get distinct columns/badges?

## Limitations / UNVERIFIED

- OpenCode routes and semantics were verified by reading the v1.19.4 source at
  `148842ef…`; OpenCode was not run, so live status codes, error bodies for an
  unknown session on `/session/:id/diff` (the handler does not call
  `requireSession`; `handlers/session.ts:99-104`), and workspace-query behavior
  are **UNVERIFIED**.
- The "Claude Code coder view" itself was not inspected; the requested
  information set (D9) is implemented as described, not copied.
- ADR-021's history store does not exist yet (issue #152, PROPOSED); this ADR
  defines only the interface boundary.
- The manager has no execution-list method today; the read-only shape above is a
  design, and its exact locking must be reviewed with the manager's lock order.
- Whether `config.snapshot === false` is reachable through supported settings is
  inferred from `summary.ts:115`; the settings surface was not traced.
- Browser compatibility, mobile layout and accessibility were not tested.
