# ADR-023: read-only session dashboard

Date: 2026-11-11. Status: **ACCEPTED (per D12, 2026-11-11, Planning Desk;
reviews #161/#169, merged #157)**. Implementation proceeds in the slices of §7;
this change adds slice 1 (skeleton) only.

**Acceptance note (2026-11-11).** The Planning Desk accepted this ADR under
decision D12 (ADR acceptance delegated to the Planning Desk; no D12 exception
applies): review #169 (A) is PASS-WITH-NITS on `4742b04` with no P0-P2, the
recommendation conforms to D9 (a separate read-only dashboard), and PR #157 is
merged (`12fea90`). At that date the open questions stayed open: acceptance did
not decide them. **They were answered by maintainer decision D17 on 2026-11-12
(dated note below; section "Decided (D17, 2026-11-12)").** The #169 nits R1-R3
are fixed in this revision, the Bridge file:line
citations are re-pinned to main `12fea90` (the OpenCode citations stay pinned to
`148842ef`), and the audit-ring attribute allowlist is enumerated in §1.1.

**Decision note (2026-11-12, planner task #220).** The maintainer answered all
nine open questions in decision D17 (recorded in issue #117), all as
recommended; the answers are in "Decided (D17, 2026-11-12)" and replace the old
"Open questions" section. This note, the sentence of the acceptance note above
that said the questions stay open, and that section are the only edits made for
it: no other decision, the threat model, the slice plan and every other section
are unchanged. References elsewhere in this ADR to "open question N" point to
the item of the same number in the Decided section.

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
| Executions (id, session, state, started/updated/ended, unresolved, permission rejection, guard-release time) | The execution manager's in-memory records (`internal/execution/manager.go:53-71`) | The dashboard needs a new **read-only enumeration** (`DashboardSnapshot`) that returns copies; today the manager exposes only per-ID `Snapshot`, `Get`, `SessionGuard` (`manager.go:492`, `:511`, `:532`). Lock order (review #161 F3, corrected by review #169 R1): existing code either **nests** the two mutexes in the order run mutex then manager mutex (`finalizeUnsubmitted`, `manager.go:369-391`, takes `x.mu` at `:370` and `m.mu` at `:387-391`; `commitRelease`, `release.go:280-285`) or holds **one at a time** (`SessionGuard`, `manager.go:505-527`, whose comment says it never holds `m.mu` and `run.mu` together). The enumeration copies the run pointers under `m.mu`, **releases `m.mu`**, then locks each run's `x.mu` alone for the copy, so it holds one mutex at a time and cannot invert the nested order. It never takes `x.op`. |
| Current execution | The newest non-terminal execution for the session, by `Started` | Computed in the view model, not stored. |
| Guard | `Manager.SessionGuard(session)` (`manager.go:511-527`) | Shows `held` plus the state that holds it. |
| Audit | AUDIT-01 lines (`docs/CONTRACTS.md:188`), including the admin `guard_release` lines | The dashboard replaces the process default logger with a **fan-out handler**: the original journal handler stays one branch (no double output), and the second branch copies into a bounded in-memory ring (1000 entries, drop-oldest). Because AUDIT-01 is written through the package-global `slog.Info` (`internal/mcp/audit.go:160`) and the admin server's `slog.Default()` (`internal/admin/admin.go:191-195`, `logger()`), the tee sees **every** slog record; it therefore keeps only records whose `event` attribute is in a closed allowlist (`guard_release`, `prompt_submission`, `interrupt`, and the future lifecycle/permission events) and copies only an allowlisted attribute set. **The attribute allowlist (enumerated from the existing AUDIT-01 writers, `internal/mcp/audit.go:117-160` and `internal/admin/admin.go:310-340`):** `event`, `tool`, `outcome`, `accepted`, `error_code`, `session_id`, `execution_id`, `idem` (the short idempotency tag, never the key), `principal`, and for `guard_release` also `native_state`, `unfinished`, `permission_pending`, `correlated_user`, `settle_ms`, `allow_missing_user_message` and `ticket`; plus the record time and message. Any other attribute is dropped, and a future event name or attribute is not shown until this list is amended. The admin server must be constructed with the process default logger (or the same tee), not a separate one. Everything else is discarded. It never re-reads the journal, never opens the journal file, and never persists entries. The ring is display-only and is lost on restart, which is acceptable because the journal remains the audit authority. |

### 1.2 Native OpenCode (v1.19.4, `148842ef7cb1bc34192b3fff57d0f9721ab9c801`)

All paths below are in `anomalyco/opencode` at that exact commit, verified by
source reading only (OpenCode was not run).

| Data | Route | Source | Caveats |
| --- | --- | --- | --- |
| Session list | `GET /session` | `packages/opencode/src/server/routes/instance/httpapi/groups/session.ts:111` | Instance-scoped; workspace-bound sessions need the `directory`/`workspace` query fields (`middleware/workspace-routing.ts:22-25`). |
| Session detail | `GET /session/:sessionID` | `.../groups/session.ts:132` | Root/cwd from the session record. |
| Status map | `GET /session/status` | `.../groups/session.ts:121` | OpenCode **deletes** idle entries (`packages/opencode/src/session/status.ts:32,42-44`), so absence means idle; the dashboard must not report `running` from absence (same rule as the Bridge's ADR-017/018 logic). |
| Messages | `GET /session/:sessionID/message` | `.../groups/session.ts:179` | Needed to enumerate user-message IDs for diffs. |
| **Session diff** | `GET /session/:sessionID/diff?messageID=<id>` | `.../groups/session.ts:84` (path) and `:168-178` (endpoint); handler `.../handlers/session.ts:99-104`; query `DiffQuery` `:39-42` | **Per user message, not session-wide.** `SessionSummary.diff` returns `[]` when `messageID` is omitted (`packages/opencode/src/session/summary.ts:129-131`) and otherwise returns the diffs stored on that user message (`:135-141`). The diffs are computed at summarize time from `step-start`/`step-finish` snapshot hashes (`:82-100`) and stored on the user message (`:102-127`); if the snapshot feature is disabled (`config.snapshot === false`, `:115`) the result is always empty. Response items are `Snapshot.FileDiff`: optional `file`, optional `patch`, `additions`, `deletions`, optional `status` added/deleted/modified (`packages/schema/src/file-diff.ts:6-12`). The route's `[]` is ambiguous: it is the same for "no changes", snapshot disabled, an absent/wrong `messageID` and a message whose diffs were never computed (review #161 F2). The dashboard must therefore first read each user message's native `info.summary.diffs` from the message list: `summary`/`diffs` absent → *not computed / unavailable*; `diffs: []` → *computed, no changes*; non-empty → call the diff route with that `messageID`. Three fixtures must render three distinct labels (*computed, no changes*; *not computed*; *changes*), and **"unavailable" (timeout, error, snapshot disabled) is a fourth, separate label** (review #169 R3). |
| Working-tree diff | `GET /vcs/diff?mode=git\|branch&context=N` and `GET /vcs/diff/raw` | `.../groups/instance.ts:48-49` (paths), `:104-121` (endpoints); handler `.../handlers/instance.ts:51-59`; implementation `packages/opencode/src/project/vcs.ts:235,373-390` | Repo-level VCS diff (`mode=git` or `branch`), not attributable to a turn; 10 MB patch caps (`vcs.ts:12-13`). `GET /vcs/status` and `/vcs` help label it. |

**Recorded response shapes (dated sub-note, 2026-11-13, slice 3 step 0, task #240).**
Factual only; it changes no D17 answer and no other section. Source: a static
reading of the public upstream source at the same pin, `148842ef…` (paths under
`packages/opencode/src/` unless prefixed `schema/` = `packages/schema/src/`).
**Nothing below was captured from a live server: every shape is UNVERIFIED
against a running OpenCode** (the fixtures say so too).

- `GET /session/:sessionID/diff?messageID=<id>`: a JSON **array** of
  `Snapshot.FileDiff` (`schema/file-diff.ts:6-12`; endpoint
  `server/routes/instance/httpapi/groups/session.ts:168-172`): `file?` string,
  `patch?` string, `additions` and `deletions` finite numbers, `status?` one of
  `added`, `deleted`, `modified`. The query is `directory?`, `workspace?`
  (`middleware/workspace-routing.ts:22-25`) and `messageID?` (`DiffQuery`,
  `groups/session.ts:39-42`; `DiffInput`, `session/summary.ts:148-151`). The
  handler does not look the session up (`handlers/session.ts:99-104`); the service
  returns `[]` when `messageID` is absent or names no user message
  (`summary.ts:130-134`), else `message.info.summary?.diffs ?? []` with each
  `file` path-unquoted (`summary.ts:135-141`).
- `GET /session/:sessionID/message` returns an array of `{info, parts}`
  (`SessionV1.WithParts`, `schema/v1/session.ts:493-496`; endpoint
  `groups/session.ts:179-182`). **Only a user message** can carry
  `info.summary`: `{title?: string, body?: string, diffs: FileDiff[]}`, the whole
  `summary` optional (`schema/v1/session.ts:332-345`). `summarize` stores
  `summary.diffs` (`summary.ts:125`) except when `config.snapshot === false`
  (`summary.ts:115`). So `info.summary` absent, or present without `diffs`, means
  **not computed**; `diffs: []` means **computed, no changes**.
- `GET /vcs/diff`: query `mode` **required**, `git` or `branch` (`project/vcs.ts:235`;
  `VcsDiffQuery`, `groups/instance.ts:26-30`), `context?` a non-negative integer
  (a decimal string in the URL), and `directory?`, `workspace?`. Success: a JSON
  **array** of `Vcs.FileDiff`: `file` string (required), `patch?`, `additions`,
  `deletions`, `status?` as above (`vcs.ts:246-255`; endpoint
  `groups/instance.ts:104-106`). With no `context` the patch uses
  2147483647 context lines (`vcs.ts:11,107`). The array is `[]` when the project is
  not a git project (`vcs.ts:376`), and for `mode=branch` when there is no root
  branch, on the default branch, or no merge base (`vcs.ts:381-384`): **"no
  changes", "not git" and "default branch" are the same `[]`.**
- Caps inside OpenCode: 10,000,000 bytes per patch and 10,000,000 bytes in total
  (`vcs.ts:12-13`). A patch over its cap, and every file after the total is
  reached, stays in the array with its `additions`/`deletions` and its `patch`
  replaced by the header-only `emptyPatch(file)` (`vcs.ts:18,139-147,158-164,
  182-189`). There is no truncation flag: a capped file reads as an empty patch
  with non-zero counts.
- `GET /vcs/diff/raw`: query `directory?`, `workspace?` only (no `mode`, no
  `context`); success is **plain text**, `text/x-diff; charset=utf-8`
  (`groups/instance.ts:114-117`): the tracked `HEAD` patch followed by the
  patches of untracked files, joined by `\n`; `""` for a non-git project
  (`vcs.ts:387-399`).
- The three routes run git inside OpenCode (the deployment assumption above is
  unchanged). A client that keeps the Bridge's 8 MiB response bound
  (`internal/adapters/opencode`) will fail a `/vcs/diff` answer that the
  10 MB native cap allows; the dashboard shows that as "unavailable".

**Directory query binding (review #161 F8).** Every native call that accepts a
`directory`/`workspace` query field (`/session`, `/session/:id/message`,
`/session/:id/diff`, `/vcs/*`) sends only the **ACL-validated root of the
requested session**, never a value taken from the dashboard request. An
unlisted session is `404` before any native call (T4).

**Deployment assumption for `/vcs/diff*` (review #169 R2).** The Bridge runs no
git (§1.3), but these two routes make **OpenCode** run git in the session root
(`vcs.ts:378,383,390`); whether OpenCode hardens those invocations is
UNVERIFIED. A hostile `.git/config` in a session root would therefore be
executed inside the OpenCode process, once per dashboard poll that uses these
routes. That stays inside the agent's own trust domain and is **not** the F1
boundary crossing, **provided the OpenCode user is not the Bridge user** (the
ADR-021 two-user pattern). This is a stated deployment assumption of the
dashboard, not something it enforces; it also bears on the per-poll cost
question in open question 2.

**Never exposed:** `POST /vcs/apply` (`groups/instance.ts:50`), `/session/:id/abort`,
`/session/:id/revert`, prompt routes and any other mutating route. See §4.

**Snapshot storage and bounds.** OpenCode keeps snapshot data in a shadow git
directory under `Global.Path.data/snapshot/<projectID>/<hash(worktree)>`
(`packages/opencode/src/snapshot/index.ts:71`), pruned after 7 days
(`:23`), and excludes untracked files larger than 2 MiB (`:24,278-290`). The
dashboard therefore shows a per-turn diff only when OpenCode retained the
snapshot; otherwise it must say "diff unavailable" rather than "no changes".

### 1.3 No Bridge-side git execution (v1)

An earlier draft proposed a bounded, "hardened" read-only `git diff` fallback run
by the Bridge service user. Review #161 F1 reproduced repository-controlled
command execution through `core.fsmonitor` and `filter.*.clean` even with
`--no-ext-diff --no-textconv` (git 2.43.0): a session root is agent-writable, so
that fallback would let an agent (or a prompt-injected agent) run code as the
Bridge user, which holds the connector token, the lock directory and the admin
socket.

**v1 therefore does not run git at all** — no subprocess, no repository
configuration, no hooks, no filters. If a working-tree diff is ever needed it
must run as the least-privileged harness user (the ADR-021 pattern), with
`-c core.fsmonitor=false`, `-c core.hooksPath=/dev/null`,
`--no-ext-diff --no-textconv`, `GIT_OPTIONAL_LOCKS=0`, and a refusal of any root
whose config or attributes define `core.fsmonitor`, `filter.*`, `core.hooksPath`
or `include.*`; that needs its own review and a canary test proving an
`fsmonitor` or clean-filter config writes no file. It is **not** in this ADR's
v1. The dashboard therefore shows diffs only from the native session route, the
native `/vcs/diff` route, or the ADR-021 history (Claude Code).

### 1.4 Claude Code

**Dependency, not a supplied feature (review #161 F6).** ADR-021 (PROPOSED,
issue #152) defines a Bridge-owned append-only history of `claude -p`
stream-json events, but as proposed it has **no dashboard interface**, it stores
bounded tool inputs/results rather than patches, and its first deliverable uses a
read-only tool profile (`Read,Glob,Grep`) that emits **no file-edit events**.
The dashboard's Claude diff source is therefore an **open requirement on
ADR-021**, not something that ADR provides: ADR-021 must add a read-only query
(and, if wanted, file-edit capture) or the dashboard shows "diff unavailable
(ADR-021 pending)" for every Claude session. This ADR does not define that
schema and does not parse `~/.claude` JSONL.

Pre-Bridge turns are unavailable by construction (ADR-021, D7); the dashboard
shows ADR-021's explicit "history before first Bridge resume is unavailable"
marker.

### 1.5 Precedence and labelling

Per session, the dashboard shows the best available source and names it:

1. native per-turn session diff (OpenCode) → label "turn N, native snapshot";
2. `/vcs/diff` working tree (OpenCode) → label "working tree (not per turn)";
3. ADR-021 history file-edit events (Claude Code) → label "Bridge history",
   if and when ADR-021 provides the query.

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
to a local socket inside an operator-owned private directory (mode `0700`),
never a world-writable `/tmp` — for example
`ssh -L ~/.harness-bridge/dash.sock:/run/harness-bridge-dash/dashboard.sock` — or
a local `socat` proxy. OpenSSH's own mode for the forwarded local socket is
UNVERIFIED; the private parent directory is the fence. This is strictly stronger
than 127.0.0.1 and adds no credential.

### Option (b): on the public HTTPS listener behind its own dashboard auth

- Pros: reachable from any device without an SSH tunnel; one port.
- Cons: puts repository content behind a second credential and a second auth
  code path; the cookie must be rotated and protected (Secure, HttpOnly,
  SameSite=Strict) alongside the connector token (ADR-019); an auth bypass or
  XSS becomes remotely exploitable; CSRF risk is low because everything is GET,
  but session fixation and cache leakage are new concerns; it couples the
  dashboard's release cadence to the connector listener and its tests.
- Recommendation: **not for v1** (recommended against). A future remote need
  gets its own ADR with a token/cookie design, not a silent expansion of this
  one.

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
  img-src 'self'; connect-src 'self'; frame-ancestors 'none';
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

- The page polls `GET /api/state` with `If-None-Match: <etag>` every 2 s while
  visible and every 10 s when hidden (Page Visibility API); the list view polls
  every 5 s.
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
│ claude    claudecode:4e59c1bd      /opt/…/proj-x        idle       │
│ ─────────────────────────────────────────────────────────────────  │
│ last update 12:04:11Z · polling 2s · diff source: native snapshot   │
└─────────────────────────────────────────────────────────────────────┘
```

Columns: harness, session ID + title (truncated), root, state, guard, current
execution ID, last activity, changed-files count if a diff is available.
Filters are client-side only (no query-driven server work beyond pagination
limits). Session `state` is one of `idle|running|waiting_for_permission|unknown`;
`completed` is an **execution** state (`docs/CONTRACTS.md:594`, "Session state
and the Bridge guard"), never a session state. `idle` is **not proof that the
previous task succeeded** (ADR-011) and must not be rendered as success (review
#161 F5). What it means depends on the harness (review #169 R3): for an
OpenCode session with no Bridge guard it comes from the native status, an
explicit `idle` **or an absent entry** (OpenCode deletes idle entries,
`docs/CONTRACTS.md:594-616`); for a Claude Code session it means only that the
Bridge holds no guard and runs no child.

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
| T4 | Cross-session access / path traversal | reuse the ADR-012 operator ACL; unknown or unlisted session → 404; IDs validated with the existing patterns; never build filesystem paths from native strings; native `directory`/`workspace` queries come only from the ACL-validated root | request a session outside the ACL → 404; `../` in IDs → 400/404; a dashboard request cannot influence the native `directory` query |
| T5 | CSRF | no state-changing route exists; GET/HEAD only; no cookies in (a′) | route-table test + mutating-method sweep → 405 |
| T6 | Prompt injection through rendered content | the dashboard never feeds displayed content back to an agent; no LLM endpoint; no buttons that call MCP | code review + route table: no route accepts prompt input |
| T7 | Resource exhaustion | caps (§3), timeouts (5 s), single-flight diffs, 4 concurrent requests, 503 + Retry-After | oversized/binary/truncated fixtures; slow native fake hits the 5 s bound and renders "unavailable" |
| T8 | Clickjacking | `frame-ancestors 'none'`, `X-Frame-Options: DENY` | header test |
| T9 | DNS rebinding / CORS | Host allowlist; no CORS headers; no `Access-Control-Allow-Origin` | Host/Origin tests |
| T10 | Locking out or stalling writers | the dashboard's read-only enumeration copies run pointers under `m.mu`, releases it, then locks each `x.mu` alone; it never holds `m.mu` and a run mutex together and never takes `x.op`; native reads use their own bounded client, not the execution manager's slots | lock-order test: enumeration concurrent with `send_prompt`/finalize under a 5 s deadline (the race detector does not find lock-order inversions); a fake holds a native read; a concurrent `send_prompt` still proceeds |
| T11 | Mutation by accident | read-only interface type; no mutating methods reachable from the dashboard package | mutating-method sweep + journal test: no `prompt_submission`/`interrupt`/`guard_release` line caused by dashboard traffic |

## 7. Implementation plan

Each slice is one PR, tests first (RED before GREEN where practical), with a
mutation check and an acceptance step. No slice changes the MCP tool contract.

| Slice | Scope | RED tests / mutations | Acceptance |
| --- | --- | --- | --- |
| 1 | `internal/dashboard` skeleton: config (`BRIDGE_DASHBOARD_SOCKET`/`ADDR`, disabled by default), listener validation (0700 unix socket or loopback only), route table, CSP/middleware, read-only `Source` interface | route-table test fails if any non-GET route exists; bind `0.0.0.0` refused; mutation: add a POST route → test RED | server starts on a 0700 socket, `curl` GET `/` returns HTML with CSP; TCP bind to 0.0.0.0 exits with an error |
| 2 | Sessions list + detail view model; read-only manager enumeration | XSS fixtures RED; mutation: remove escaping → fixture test RED; lock-order test (enumeration vs `send_prompt`/finalize, 5 s deadline) RED on an `m.mu`→`x.mu` inversion; manager method copies, no aliasing | list matches MCP `list_sessions` for the ACL; detail shows state/guard/current execution |
| 3 | Diffs: OpenCode per-turn session diff (enumerate user messages, read `info.summary.diffs` presence), `/vcs/diff`, caps, binary, redaction; **no Bridge-side git subprocess**; Claude Code is an open ADR-021 requirement | `summary.diffs` fixtures (undefined / `[]` / non-empty) RED; mutation: treat undefined as empty, or add a `git` shell-out → test RED; a session root with `core.fsmonitor` and `filter.*.clean` canaries must stay unwritten because nothing runs git | diff matches `GET /session/:id/diff` for a known turn; the four labels (computed-empty, not-computed, changes, and unavailable for timeout/error); no subprocess is spawned |
| 4 | Audit ring (tee handler with `event`/key allowlist), polling JSON + `ETag`/`If-None-Match`, single-flight, 503 | mutation: add a mutating method to the interface → compile/test RED; journal test | audit feed shows AUDIT-01 lines; no mutation lines from dashboard traffic |
| 5 | Hardening sweep (Host/Origin, timeouts, race), docs, live acceptance | full route sweep; race detector | live acceptance below |

### Live acceptance outline (R-DASH)

1. Start the Bridge with the dashboard **disabled**; confirm no socket/port.
2. Start it with `BRIDGE_DASHBOARD_SOCKET` in a 0700 directory; confirm the
   directory and socket modes, and that no TCP port is open.
3. Forward the unix socket over SSH (or use `socat`) and open the list; compare
   every row's session/root/state with `list_sessions`/`get_session` over MCP.
4. Send a prompt through MCP that writes a known file; within two polls the
   detail view shows the new turn and its diff; compare with the native
   `GET /session/:id/diff` response for the same user message and with the
   `info.summary.diffs` presence rule (F2). Put a hostile `core.fsmonitor` and
   `filter.*.clean` config in the root: dashboard traffic must write no canary
   because it runs no git (F1).
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
- Adds no MCP tool, no database, no external service, no Node build chain, and
  no git subprocess.

## Decided (D17, 2026-11-12)

The maintainer answered every open question of the proposal in decision D17
(2026-11-12, recorded in issue #117), all as recommended. The questions are kept
in short form with the answer.

1. **Transport.** **Unix socket plus SSH `-L` forwarding**; the host is a single
   operator. No `127.0.0.1` token transport.
2. **Diff source default.** The detail view defaults to the native working-tree
   `/vcs/diff`; **per-turn diffs load on demand** and are cached for the poll
   interval.
3. **ADR-021 dependency.** **Claude Code diffs show as unavailable** until a
   later ADR; ADR-021 is not required to add a dashboard query now.
4. **Audit scope and retention.** A **1000-entry ring**, including the admin
   `guard_release` lines, **drop-oldest**.
5. **Redaction.** **On by default**, with a shipped default list and patterns in
   the operator configuration.
6. **Session scope.** **All ACL-authorized sessions plus the ADR-022 lifecycle
   roots.**
7. **ADR-022 archived sessions.** **Hidden by default, with a filter.**
8. **Multi-harness display.** **Distinct harness badges and columns.**
9. **Working-tree diff without Bridge-side git.** The **native `/vcs/diff` is
   sufficient**; there is no Bridge-side git fallback.

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
  design. Its lock order is specified (copy under `m.mu`, release, then each
  `x.mu` alone) and must be pinned by the slice-2 lock-order test.
- Whether `config.snapshot === false` is reachable through supported settings is
  inferred from `summary.ts:115`; the settings surface was not traced.
- No Bridge-side git: the working-tree diff depends entirely on the native
  `/vcs/diff` route (or a future, separately reviewed least-privilege fallback).
- The ADR-021 dashboard interface does not exist; Claude diffs are an open
  requirement, not an available source.
- Unix-socket listener residual (slice 1 review #194 P3-2/P3-3). The socket
  directory is checked (owner, mode exactly 0700, opened with `O_NOFOLLOW`) and
  then the socket is bound **by path**. `O_NOFOLLOW` refuses a symlink only as
  the **final** directory component; ancestor components are followed, as for the
  ADR-020 admin socket. After the bind the directory is opened again and must
  have the same device, inode, owner and mode, and the socket must be a socket
  owned by the service user, otherwise startup is refused: this detects a swap
  that happened before the re-check. It does not close the window after it, and
  an ancestor or the directory itself that another user (or root) can replace is
  outside what the dashboard can defend: the **same-UID/root residual of the
  ADR-020 trust boundary**, an operator duty (the socket directory and its
  ancestors must not be replaceable by agent users). The TCP transport accepts
  only `127.0.0.1` and `::1`, the two literals on the Host allowlist.
- Browser compatibility, mobile layout and accessibility were not tested.
