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

**As built (slice 3, dated 2026-11-13, task #240).** Facts about the code, no new decision;
every D17 answer, the threat model and the other sections are unchanged.

- **Labels.** `changes`, `computed, no changes`, `not computed`, `unavailable`. A turn's
  label comes from the presence of `info.summary.diffs` in the message list before any diff
  read: `not computed` (summary absent, or present without `diffs`) and `computed, no
  changes` (`diffs: []`) make **no** native diff read and `not computed` is never rendered
  as "no changes"; only a turn with changes is read (`GET /session/:id/diff`, on demand,
  from `/session/<harness>/<id>/turn/<messageID>`). A read that times out or fails is
  `unavailable` with the closed reason `timeout` or `error` (the error text is never shown);
  an empty answer for a turn reported as having changes is `unavailable` too. The default
  source of the detail page is the native working-tree `/vcs/diff?mode=git` (D17 Q2, Q9),
  labelled "working tree (not per turn)"; its empty answer is labelled `computed, no changes`
  **with the caveat** that OpenCode returns `[]` for a non-git project and the default branch
  as well. `/vcs/diff/raw` and `mode=branch` are not used. Claude Code sessions show "diff
  unavailable (ADR-021 pending)" and make no native diff read (D17 Q3).
- **Caps.** At most 200 files per diff, 256 KiB of patch per file, 1 MiB of patch in total,
  512 runes of file path, 100 turns listed; the existing 5 s request bound and 2 MiB page
  bound apply. Every omission is written on the page ("truncated (per-file cap ... total
  cap ...)", "N more files not shown", "not shown: total cap ... reached"). Binary: a NUL
  byte in the first 8 KiB, git's own `Binary files` notice, or invalid UTF-8 above 10 % of
  the first 8 KiB renders "binary file, not displayed (N bytes)" with no content. A patch
  with no hunk but non-zero counts is shown as "patch not provided" (it is what OpenCode
  substitutes when its own 10,000,000-byte cap applies). The Bridge's OpenCode client keeps
  its 8 MiB response bound, so a `/vcs/diff` answer above it is `unavailable`.
- **Cache.** A diff read is cached for 2 s (the detail poll interval), at most 64 entries,
  one in-flight read per key (single-flight, T7); memory only, never written to disk.
- **Redaction** is **on by default** with no configuration (D17 Q5): `sk-ant-`, `ghp_`,
  `github_pat_`, `AKIA[0-9A-Z]{16}`, private-key blocks (an unterminated block is redacted
  to the end), `Authorization: Bearer` and `Bearer <token>`. It runs on file names and on
  patch text **before** the per-file cut (with a 4 KiB overlap, so a token that straddles
  the cut is replaced whole) and before rendering; the count is shown ("redaction on: N
  values redacted"), never logged. The operator hook is `Options.Redactor` (up to 16 extra
  RE2 patterns of at most 256 bytes); it is **not** read from an environment variable or a
  file yet.
- **No git.** No file of `internal/dashboard` imports `os/exec`; a test sweeps the imports
  and the call names, and a session root with `core.fsmonitor` and `filter.*.clean` hooks
  stays unwritten across every route.
- **Semantics (review #223).** P3-1: a data source that fails is HTTP 503 with
  `Retry-After: 5` for the list and the detail alike; a source that is configured absent
  (`NullSource`) is a 200 page. P3-2: `execution.Manager.DashboardSnapshot` deliberately
  takes no context and no cap: it does no I/O, and the manager's own memory-only maps are
  not capped, so a cap would hide executions instead of bounding anything; the pointer copy
  under `m.mu` is O(runs) and a test pins 20,000 runs well inside the 5 s bound.
- **Not in slice 3:** a production `Source` (the Bridge still wires `NullSource`), reading
  the operator redaction patterns from configuration, the audit ring and polling (slice 4).

**As built (slice 4, dated 2026-11-14, task #250).** Facts about the code, no new decision;
every D17 answer, the threat model and the other sections are unchanged.

- **Process logger (measured on `e81d445`).** `cmd/bridge` builds no slog handler and calls
  no `slog.SetDefault`: the process default is Go's default handler, which writes through the
  `log` package to stderr (the journal). AUDIT-01 is written by the package-global
  `slog.Info` (`internal/mcp/audit.go:113-160`, the call at `:160`); the admin server calls
  `s.logger().Info` (`internal/admin/admin.go:338`, writer `:310-338`), and `logger()`
  (`:190-194`) returns `slog.Default()` evaluated at each call, so it follows a replaced
  default. `internal/security/remote.go` also logs through `slog.Info` (no `event`
  attribute: the ring drops those lines, the journal keeps them).
- **Tee.** `dashboard.InstallAuditTee(ring)` replaces the process default logger with a
  fan-out handler: the journal branch writes each record the original default handler enables,
  once and untouched, and the second branch copies an allowlisted projection into the ring.
  **The swap is unobservable to concurrent loggers (review #252 P2-1, fixed after PR #251's
  first head `6ed96d2`).** `slog.SetDefault` also points the `log` package at the new handler,
  and Go's default handler writes through the `log` package's global logger; a journal branch
  made of that handler therefore called the `log` lock that the call being handled already
  held, and any `log` or `slog` call between `SetDefault` and the restore of the `log` writer
  deadlocked the global logger (reachable at startup, the admin server being up, and at
  shutdown). The journal branch no longer touches the global logger: it formats a record as
  the default handler does (`LEVEL message` and the attributes in the `slog.TextHandler` text
  form, the same code) and writes it with a private `log.Logger` on the original writer,
  prefix and flags, behind a lock it shares with the `log` package's own output so lines never
  interleave. On restore `slog.SetDefault` is given the original default handler, which does
  not touch the `log` package. Tests: a record-by-record byte comparison with the default
  handler (levels, quoting, groups, `With`, built-in attribute names, multi-line and empty
  messages, flags and prefix), golden tests over the real mcp and admin writers, a test that
  logs through `log` and `slog` **inside** the install and the restore windows (a test seam)
  with a watchdog, and a `-race` test of 200 install/restore cycles under eight concurrent
  loggers. **What a line logged in the install window looks like:** a `log.Printf` that runs
  between `SetDefault` and the restore of the `log` writer is converted by the `log` package
  into an Info record, goes through the tee and is written as `INFO <message>` (the same
  message with the level word a plain `log` line does not carry); every other line is
  byte-identical. Not supported and not needed by the Bridge: the `log` flags `Lshortfile` and
  `Llongfile` (a journal line would name the tee, not the caller), a second tee installed over
  the first, and a program whose own default handler writes through the `log` package. It is
  installed by `openDashboard` only when the dashboard is enabled (a disabled or refused
  configuration touches nothing) and put back by the server's `Shutdown`.
- **Ring.** 1000 entries, drop-oldest, newest first; memory only; written only by the tee (the
  write method is unexported and the dashboard reads through an `AuditSource` interface with the
  single method `Entries`, pinned by a test). Kept events: `guard_release`, `prompt_submission`,
  `interrupt` (the lifecycle and permission events of the plan are **not** shown until the list is
  amended). Kept attributes: the allowlist of section 1.1. Any other attribute is dropped, and so
  is every attribute namespaced by `WithGroup` or inside a `Group` value; attributes bound with
  `With` before any group are filtered the same way. Only plain strings, booleans and integers are
  kept; each value is made valid UTF-8, stripped of control characters, passed through the default
  redactor and cut at 256 bytes; the record message gets the same treatment. The ring branch
  recovers from a panic (a `LogValuer` that panics) and can never fail a log call.
- **Audit views.** `GET /audit` (a table, newest first, a browser-side text filter, every value
  escaped by `html/template`) and `GET /api/audit` (JSON `{enabled, limit, entries[{time, message,
  event, attrs[{key, value}]}]}`). Without a feed (`Options.Audit` nil) both say so. Dashboard
  traffic writes nothing to the ring or the journal (tested over every route and method).
- **Polling JSON.** `GET /api/state` (list) and `GET /api/state/session/<harness>/<id>` (detail)
  return the same normalized, bounded values as the HTML pages; the detail model carries the
  session, guard, current execution and executions and **no diff content** (diffs keep their own
  pages and cache). Every model response has a strong `ETag` (a hash of the body) and
  `Last-Modified` (the time the body last changed, so an unchanged rebuild keeps both);
  `If-None-Match` (a list, `W/` and `*` accepted, a header over 4 KiB ignored) or, when absent,
  `If-Modified-Since` gives `304` with no body. A model is built at most once a second per key
  (the 1 s cache of section 4), concurrent builds of one key share one read (the diff cache's
  single-flight, now generic and reused), at most 64 keys, memory only; the build runs under its
  own 5 s bound detached from the first caller so one client leaving cannot hand its cancellation
  to the polls sharing the read. A failing source is `503` with `Retry-After: 5` and a fixed JSON
  body, an unknown or invalid session `404` with a fixed body; errors are not echoed.
- **Page script.** `dashboard.js` (still the one embedded script, no inline script, no new origin,
  CSP unchanged) polls with `GET` and `If-None-Match`: the list every 5 s, the detail every 2 s
  while visible and every 10 s when hidden, one self-rescheduling timer, nothing on a failed
  poll. It sets every value with `textContent` (no markup from data, no evaluation) and accepts a
  link only when it starts with `/session/`. It updates the list and the detail's fields and
  executions; the diff section is not polled (reload). A page rendered with no executions reloads
  once when executions appear. The audit page's filter hides rows in the browser only. The page
  works with scripts disabled.
- **Limits.** Every routed request runs under a 5 s context. At most **4** requests that read the
  data source (`/`, the session pages and the polling JSON) are served at once, and at most **8**
  requests of any kind are in flight (those plus the audit pages and the assets); the 5th data
  request or the 9th in-flight request gets `503`, `Retry-After: 1` and the fixed body `busy`
  immediately, never a queue; HEAD counts like GET; unknown paths and refused methods are answered
  before the limiter and take no slot.
- **Carried from review #243.** P3-1: the detail's 503 now has `Retry-After: 5`. P3-2: the 2 MiB
  bound applies to the escaped page; a page over it hides, from the last file, the patches that do
  not fit (each hidden patch says "patch not shown: the escaped page would exceed the 2 MiB page
  bound") and is served; only a page that is still over the bound with no patch at all is refused.
  P3-3: the labels of the detail's turn list are pinned by a test.
- **Not in slice 4:** a production `Source` (the Bridge still wires `NullSource`, so the R-DASH
  acceptance stays NOT_EXERCISED), reading operator redaction patterns from configuration, SSE, and
  any change to AUDIT-01, the MCP contract or `internal/adapters/claudecode`. The page script was
  syntax-checked with `node --check` and exercised against a stub DOM, **not in a browser**.

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

## As built (slice 5a, 2026-11-15)

Facts only. The production `Source` for OpenCode sessions, its wiring, the operator redaction
configuration and the Host/Origin hardening are implemented; the live acceptance (R-DASH) is
**NOT_EXERCISED**.

- **Source** (`internal/mcp/dashsource.go`, `(*Server).DashboardSource`). It lives in `internal/mcp`
  so that the list equals `list_sessions`: it runs the same `discoverNative` pipeline over a derived
  server whose ACL copy omits `claudecode:` IDs (own two discovery slots; the derived server is
  `ReadOnly` and shares the client and the manager). Rows: the OpenCode sessions in `list_sessions`
  order (sorted by native ID), then one pending row per `claudecode:` ACL entry (harness
  `claudecode`, state `unknown`, no title or root); the detail page shows the documented "ADR-021
  pending" text and its turn/diff methods answer `ErrNotFound`. A `claudecode:` ID never reaches
  the native client.
- **Detail** uses `authorized` + `describe` (the `get_session` path) and the manager's
  `DashboardSnapshot` for guard, executions (newest first) and the current execution (newest
  non-terminal; `unknown` counts as non-terminal). `FORBIDDEN`, `NOT_FOUND` and `INVALID_INPUT`
  from native map to `ErrNotFound`; every other error is "unavailable". An ID outside the ACL is
  refused before any native call. Last activity is the native `time.updated` when it is a usable
  value, else absent.
- **Turns and diffs**: `Client.History` (8 MiB client bound) lists the user messages with
  `MessageInfo.DiffState`; the newest 1000 turns are returned. `Client.SessionDiff` reads one turn;
  `Client.VCSDiff(mode=git)` reads the working tree through the directory-scoped client of the
  session. No new native endpoint, no Bridge-side git, no mutating call.
- **Archive**: `DashboardSource` takes an optional `ArchiveOverlay` (nil in production, so nothing is
  hidden; no overlay exists yet).
- **Wiring** (`cmd/bridge/dashboard.go`): the source factory is called only when the dashboard is
  enabled. A disabled dashboard builds no source, opens no listener and starts no goroutine.
- **Operator redaction (D17 Q5)**: `BRIDGE_DASHBOARD_REDACT_EXTRA`, a JSON array of up to 16 RE2
  patterns of 1 to 256 bytes, **additive** to the shipped list (which is on with no configuration).
  A value that does not parse or compile is a startup error; errors and logs never contain the
  value. This env name is a slice-5a proposal, not an ADR decision; it is easy to rename.
- **Origin**: a request with an `Origin` header is refused (403) unless its host is on the TCP Host
  allowlist; on the unix socket (no allowlist) any `Origin` is refused. Requests without `Origin`
  (curl, same-origin GET) are unaffected. Host checks, method limits and timeouts are the slice 1 to
  4 ones.
- **Not in slice 5a**: lifecycle-root rows (D17 Q6, after ADR-022 S5), an archive overlay, Claude Code
  history and diffs (ADR-021 pending), `list_catalog`-equivalent filters, documentation of the
  dashboard variables in `docs/CONTRACTS.md` and the deployment docs, and R-DASH (operator-present
  live acceptance against the real native and a browser). The native response shapes remain
  UNVERIFIED against a live server.
- **2026-11-15, merge and review fixes (task #273).** Main `1a571fc` (ADR-021 wiring, PR #264) was merged
  into the slice-5a branch (one `mcp.Server` value serves `/mcp` and the dashboard source; the Claude
  adapter is set on it only when enabled). The ordering above is unchanged and now checked with the
  adapter on and off: the OpenCode rows equal the OpenCode rows of `list_sessions`, and the Claude
  session is one pending row after them (`list_sessions` sorts every row by session ID instead). Review #272
  fixes: the dashboard redactor (shipped list plus `BRIDGE_DASHBOARD_REDACT_EXTRA`) is also given to the
  audit tee, so the audit ring (`/audit`, `/api/audit`) redacts operator patterns too while the journal
  line stays byte-identical; `BRIDGE_DASHBOARD_REDACT_EXTRA=null` is a startup error.

## As built (slice 5b, 2026-11-15)

Facts only (task #289). R-DASH stays **NOT_EXERCISED**; the native shapes stay UNVERIFIED live.

- **Lifecycle-root rows (D17 Q6) are a label, not more rows.** `DashboardSource` takes the lifecycle
  roots (and the overlay) from the Server's `Catalog` configuration when it is set; each row gets
  `LifecycleRoot`, the root that lexically contains the session directory (`LifecycleRoots.RootOf`, the
  value `list_catalog` returns as `root`). The rows are still exactly the ACL sessions: this is the
  rule of ADR-022 6.1 ("they widen nothing"), and a created session that is not in the ACL is not
  shown, because reading it would need the O2 flag (D16 Q7), which is out of scope. **Author choice
  for the maintainer to confirm:** if D17 Q6 meant "also sessions under a lifecycle root that the ACL
  does not list", that needs O2 and a separate task. Nothing wires `Server.Catalog` in `cmd/bridge`
  yet, so in production the label is empty until the lifecycle wiring task.
- **Archived filter (D17 Q7).** `Session.Archived`; hidden unless the request carries the context flag
  `WithArchived`, which the handler sets in one place from the query `archived=1` (exactly that value)
  for every data route. The `Source` interface keeps its five readers. The JSON model cache keys carry
  the flag (found while building this: without it the archived model could answer an unfiltered
  request inside the 1 s cache window; tested). An overlay that returns an error now fails the list and
  the detail (503), like `list_catalog`; slice 5a hid nothing on an error. The list links to
  `/?archived=1`, an archived row's link keeps the filter, and the poll script keeps it.
- **Hardening sweep (7 row 5, code part).** The route sweep now covers the data routes with the filter
  query for Host, Origin and method; the server timeouts and the header bound are pinned on both
  transports; `-race -count=20` runs over `internal/dashboard` and `internal/mcp`.
- **Carried P3s.** #276 P3-1: the server construction moved to `newMCPServer` in `cmd/bridge`, used by
  `main`, with a test that a nil adapter leaves `Server.Claude` a nil interface and a running one is the
  same pointer. #223 P3-1 (a failing source is 503 with `Retry-After` for the list and the detail) and
  P3-2 (`DashboardSnapshot` takes no context and no cap, a decision) were already fixed or documented
  in the slice 3 note ("Semantics (review #223)") and are pinned by tests; nothing changed.
- **Docs.** `docs/CONTRACTS.md` ("ADR-023 dashboard") and `docs/deploy/DASHBOARD.md` (variables, unix
  socket setup, SSH `-L` forwarding, redaction, what is shown). `BRIDGE_DASHBOARD_REDACT_EXTRA` is
  still marked a slice-5a proposal name.
- **Not here.** The archive overlay and ADR-022 S6/S8, the O2 flag, Claude Code history and diffs,
  any new native endpoint, and the live R-DASH run.

## Amendment A1 (ACCEPTED, 2026-11-21, maintainer decision D31): dashboard visual system v2 ("Cirak")

Status: **ACCEPTED (2026-11-21, maintainer decision D31, recorded in issue #117).** Task #463; maintainer decision **D28 (2026-11-21, in chat):** make
the dashboard look professional and reuse the *visual language* of the maintainer's Lokman admin UI.
Revised in task #469 by the **Hallmark sweep (A1.13)**, at the maintainer's request to use Hallmark for the
dashboard design, in task #477 for review #476 (one P2 and nine P3, dispositions in A1.14), in task #483 for reviews #480 and #481 (one P2 and nine P3, plus two P3, dispositions in A1.15), and in task #489
for review #486 (four P3, A1.16). **Acceptance.** The reviews passed with no open P0-P2 (C #476 and #481, B #480
and #486), the amendment conforms to the recorded D-items, and the maintainer answered **every one of its sixteen
open questions with the recommendation in decision D31 (2026-11-21, issue #117)**: A1.12 records them as decided.
Implementation proceeds in the slices of A1.9; until a slice ships, the dashboard serves the pages of the accepted
sections above.

**What it is.** Mostly a presentation amendment: tokens, layout, screens, accessibility, CSP and JavaScript
inventory, refresh behaviour, three implementation slices, a test plan and the Hallmark sweep. It supersedes
the ASCII layouts of §5 **as visual design only**; §5's data, columns and semantics stay. Reference files,
both docs only and not served: `docs/design/dashboard-v2.css` (the tokens and component shapes; slice S1
turns it into the embedded `assets/dashboard.css`) and `docs/design/dashboard-v2-mock.html` (a static mock
with fictional data that links that stylesheet). **No code changes in this task.** The design values below are
the maintainer's token values (values only); no Lokman code or asset is copied.

**What A1 decides beyond presentation** (each was confirmed by D31; none touches a recorded D-item): (i) it supersedes accepted §4's hidden-tab polling cadence with a pause (A1.7); (ii) it adds three
GET routes (`/executions`, `/catalog`, `/health`), their JSON twins, a page parameter on `/audit`, and two
`Source` readers (`Executions`, `Health`) plus liveness and pending-permission copies on the dashboard
`Execution` (A1.8); (iii) it adds a read-only getter for the connector token's **expiry timestamp** (Q4); (iv) it
adds one optional variable for an instance label, `CIRAK_DASHBOARD_LABEL`, in ADR-026's prefix (A1.8, Q3, Q16). Everything else changes
how existing data looks.

### A1.0 Invariants that stay (ADR-023 §3, §4, §6, T1-T11)

| Invariant | How A1 keeps it |
| --- | --- |
| Read-only: only GET/HEAD, no form, no write route (T5, T11) | No form, no button that submits. The only controls are links, a client-side filter, a copy button, a theme toggle and a menu disclosure, none of which sends anything to a Bridge write path. New routes are GET/HEAD. |
| Server-rendered Go `html/template` | Unchanged. No `template.HTML`/`template.JS`/`template.URL` for native data (T1). |
| No JavaScript framework, no build step, no external fetch | The one embedded `dashboard.js` stays vanilla; no bundler, no CDN, no webfont download, no remote image. |
| CSP stays strict | The header string is **byte-identical** to §3 (`default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'`). A1 adds no inline style, no inline script and no handler (A1.6). |
| Loopback / unix socket only; Host and Origin checks | Unchanged. |
| Redaction, size caps, no logging of content | Unchanged. The redaction count stays visible (A1.4). |
| Session scope: every ACL-authorized session (D17 Q6); archived sessions hidden by default with a filter (D17 Q7, ADR-025) | **The Sessions list, the session detail, `/executions`, `/catalog` and their JSON twins obey the same scope.** `/executions` shows only executions of sessions in the scope set of the Sessions list, and an archived session's executions only with `?archived=1` (A1.4 item 3, A1.8). No page widens the scope. **The audit page is the one exception, and A1 leaves it as D17 Q4 recorded it:** the 1000-entry ring is the process audit and is **not** ACL-filtered (`internal/dashboard/audit.go:78-79`: `AuditSource.Entries()` is all the dashboard gets), so it can show an entry that names a session outside the list, such as an admin `guard_release`; the sweep tests do not apply to it (A1.10 item 11). |
| Distinct harness badges and columns (D17 Q8) | The harness stays a badge plus a column (A1.4 item 1; the mock shows it). |
| ADR-010/011/017/018: nothing the page shows changes a state | The liveness and pending-permission rows render what the Bridge already holds; they are labelled advisory (A1.4). |

### A1.1 Design tokens

Dark is the default. "Extracted" = the maintainer's value (D28); "A1" = chosen here, because the extracted
list has no value for it. `docs/design/dashboard-v2.css` holds all of them as CSS custom properties.

| Token | Dark | Light | Provenance |
| --- | --- | --- | --- |
| page background | `#121212` | `#fafafa` | dark extracted; light **A1** |
| surface low (toolbars, the detail row: the second neutral) / mid (cards, top bar, tables) / high (menus) | `#1a1a1a` / `#1e1e1e` / `#242424` | `#f2f2f2` / `#ffffff` (paper) / `#ffffff` | dark extracted; light mid extracted, low and high **A1** |
| text / muted text | `#e0e0e0` / `#a6a6a6` | `#212121` / `#595959` | dark **A1**; light text extracted (neutral), light muted **A1** |
| primary (the primary action, the current selection, the focus ring) / text on primary | `#82b1ff` / `#0d1b3e` | `#2962ff` / `#ffffff` | extracted; light on-primary **A1** (the light theme blocks also set `color-scheme: light`) |
| success / warning / error / info | `#81c784` / `#ffb74d` / `#e57373` / `#64b5f6` | `#1b5e20` / `#9a4700` / `#c62828` / `#01579b` | extracted (light = the tone-text values) |
| primary pressed fill (a pressed primary button) | `#6a9cf5` | `#1a4fd6` | **A1**: the neutral pressed fill gave text on a pressed primary button 1.34:1 / 1.32:1 (review #480 P2-1); text on this fill is 6.18 / 6.70 (A1.2) |
| secondary (reserved) | `#b388ff` | `#6200ea` | extracted |
| input border | `#8a8a8a` | `#767676` | extracted |
| hairlines (decorative separators only) | `#333333` | `#e0e0e0` | **A1** |
| radius: card / pill / nav pill | 12 / 20 / 18 px | same | extracted |
| chip height / chip text / table head | 22 px / 12 px / 12 px | same | extracted |
| control height / hit target | 36 px visual / 44 px for coarse pointers | same | **A1.13** (the extracted 30 px is below Hallmark's 32-40 px app range) |
| card padding | 12 px | same | extracted |
| spacing scale | 4, 8, 12, 16, 24, 32 px | same | **A1.13** (Hallmark gate 24b: the 4 px scale) |
| type scale (one ratio, about 1.17) | 12 / 14 / 16.4 / 19.2 px: label, body, h2, h1 | same | **A1.13**; system face, tabular numerals on times, durations and figures |
| focus ring | 2 px primary, offset 2 px | same | extracted |
| motion | state only: 80 ms and 140 ms, color and background-color, never `all` | same | **A1.13** |
| type faces | `system-ui, -apple-system, "Segoe UI", "Helvetica Neue", Arial, sans-serif`; mono `ui-monospace, SFMono-Regular, Menlo, Consolas, "Liberation Mono", monospace` for IDs, paths and diffs | same | extracted; no webfont (Q2) |

There is no sidebar and so no nav width token (A1.13 shell).

**Accent restraint.** Primary marks only the primary action, the current navigation item, the current
selection and the focus ring; links are the text colour, underlined. **Reserved colour rule:** secondary
purple means **only** "pending / awaiting a human": a session or an execution in `waiting_for_permission`
and the pending-permission row. Nothing else uses it (a test fails if any other class refers to `--secondary`
or `--chip-secondary`).

**State chips** (text on a tint of its own colour at alpha 0.14). The chip shows a **sentence-case label in
the body face, untracked**, never the raw machine word, never a glyph or a dot; the label word is the
non-colour cue. The raw closed word stays in the `title` attribute and in the JSON.

| Raw state | Label | Tone | | Raw state | Label | Tone |
| --- | --- | --- | --- | --- | --- | --- |
| `completed` | Completed | ok | | `failed` | Failed | error |
| `running` | Running | info | | `interrupted` | Interrupted | warn |
| `waiting_for_permission` | Waiting for permission | secondary | | `unknown` | Unknown | warn |
| `idle` | Idle | neutral | | `archived` | Archived | neutral outline |
| any unknown value | the escaped word, as received | neutral | | | | |

`idle` is **not** success (ADR-023 §5.1, ADR-011); it stays neutral. Liveness: native status `busy` info,
`retry` warn, `absent` neutral, `unavailable` warn; `stall_reason` chips are warn with a sentence-case label
from a closed map (`COMPLETED_WITHOUT_TERMINAL_EVIDENCE` "Completed without terminal evidence",
`NO_CORRELATED_ASSISTANT` "No correlated assistant", `NO_PROGRESS` "No progress"; an unknown value renders
"Unknown"). **Reserved rows, not rendered until ADR-017 A2 is accepted and its slice ships** (A2 is PROPOSED; these
rows decide nothing for it): `PROVIDER_RETRYING` "Provider retrying", `PROVIDER_ERROR` "Provider error",
`TOOL_RUNNING_LONG` "Tool running long", `HARNESS_UNREACHABLE` "Harness unreachable"; until then such a value
renders "Unknown", and the label-map totality test (A1.10 item 7) covers the A1 values only. Audit event and outcome chips follow the same rule (a fixed label map; an unknown word is shown
escaped, neutral).

### A1.2 Contrast (WCAG 2.x relative luminance; AA = 4.5:1 text, 3:1 non-text)

Computed from the values of `docs/design/dashboard-v2.css` itself, compositing the 0.14 tint exactly (no
rounding of the tint colour): **124 token pairs** (text, muted text, six chip tones, input border, focus ring and
five semantic text colours on the four surfaces, both themes, plus text on primary and text on the pressed primary
fill) and **26 fill pairs**
(listed below), the light media-query block and the toggle block identical: **0 failures. The minimum text pair
is 4.62:1 (dark secondary chip on the high surface); the minimum non-text pair is 4.06:1 (light input border on
the low surface).** Chip tint = the chip text colour at alpha 0.14 composited over the surface the chip sits on.
Slice S1 turns this computation into a test over the embedded CSS (A1.10), so the table cannot drift from the file.

| Pair (dark) | bg | low | mid | high | | Pair (light) | bg | low | mid | high |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| text | 14.19 | 13.18 | 12.63 | 11.76 | | text | 15.43 | 14.38 | 16.10 | 16.10 |
| muted text | 7.70 | 7.15 | 6.85 | 6.38 | | muted text | 6.71 | 6.26 | 7.00 | 7.00 |
| chip ok | 7.25 | 6.59 | 6.27 | 5.81 | | chip ok | 6.05 | 5.67 | 6.30 | 6.30 |
| chip info | 6.70 | 6.11 | 5.81 | 5.39 | | chip info | 5.68 | 5.32 | 5.91 | 5.91 |
| chip secondary | 5.73 | 5.23 | 4.98 | 4.62 | | chip secondary | 5.69 | 5.32 | 5.92 | 5.92 |
| chip error | 6.86 | 6.25 | 5.94 | 5.51 | | chip error | 4.97 | 4.65 | 5.17 | 5.17 |
| chip warn | 8.22 | 7.47 | 7.10 | 6.57 | | chip warn | 4.99 | 4.67 | 5.19 | 5.19 |
| chip neutral | 10.17 | 9.19 | 8.73 | 8.06 | | chip neutral | 11.70 | 10.94 | 12.19 | 12.19 |
| input border (3:1) | 5.43 | 5.04 | 4.83 | 4.50 | | input border (3:1) | 4.35 | 4.06 | 4.54 | 4.54 |
| focus ring (3:1) | 8.64 | 8.02 | 7.69 | 7.16 | | focus ring (3:1) | 4.69 | 4.38 | 4.90 | 4.90 |

Other pairs (dark / light). **Token pairs not in the table:** semantic text on a card: ok 8.28 / 7.87, info
7.53 / 7.40, error 5.58 / 5.62, secondary 6.26 / 7.76, warn 9.63 / 6.41 (on the light low surface 7.03, 6.61, 5.02,
6.93 and 5.73); text on primary (the current nav item, the button) 7.79 / 4.90 and **text on the pressed primary
fill 6.18 / 6.70**. **The 26 fill pairs** (thirteen per
theme, the fills a component draws that the tokens alone do not give): text on a row hover 8.73 / 12.19; text on a
nav hover 9.19 / 10.94; text on the pressed fill 9.57 / 12.20; text on a diff add line 9.56 / 12.89 and muted text on
it 5.18 / 5.61; text on a delete line 9.69 / 12.67 and muted 5.26 / 5.51; text on the pending-permission row
10.06 / 12.30 and muted 5.45 / 5.35; the outline chip inside that row 4.98 / 5.92; the archived outline chip
border against a card 12.63 / 16.10 (non-text); **the pressed primary fill against the surface it sits on, the
toolbar `low` surface and the card (non-text, 3:1): dark 6.37 and 6.10, light 5.99 and 6.70** (four of the 26).

**Where the extracted values were not enough (the reasons for the "A1" cells):**

1. **Error chip text.** The extracted error colour as chip text gives dark 4.55:1 on a card but **4.24:1 on
   the `high` surface (fails)**; light `#c62828` gives **4.498:1 on white (a hair below 4.5: it fails)**, 4.32 on
   the page background `#fafafa` and 4.05 on the low surface `#f2f2f2` (fail). The chip text therefore uses `#ef9a9a` (dark) and `#b71c1c` (light); the error
   tint behind a chip and behind a removed diff line is that same colour at alpha 0.14. Error as plain text
   (the `-M` count, an error message) keeps the extracted value.
2. **Light surfaces.** The light page background `#fafafa` and the second neutral `#f2f2f2` are A1 values:
   the maintainer's list has only the paper `#ffffff`. Every pair above holds on all four surfaces.
3. **Nested tints.** A chip inside the tinted pending-permission row would stack two tints (dark 3.96:1,
   fails; light 4.61); inside that row chips are outlines (4.98 dark / 5.92 light).
4. **Pressed primary button (review #480 P2-1).** `.btn:active` gave every button the neutral pressed fill, and its
   specificity (0,2,0) out-ranks `.btn--primary` (0,1,0), so a pressed primary button drew `--on-primary` text on
   `--hairline`: **1.337:1 dark, 1.320:1 light**
   (unreadable). *After:* a `.btn--primary:active` rule, placed after `.btn:active`, uses the new `--primary-active`
   token (`#6a9cf5` dark, `#1a4fd6` light); text on it is **6.18 / 6.70**, the pair is in the token set and in the
   contrast test, and the mutation "primary pressed fill reverted to `--hairline`" is listed (A1.10). **The 3:1
   non-text pair of the press is the pressed fill against the surface it sits on** (toolbar `low` / card): dark
   6.37 / 6.10, light 5.99 / 6.70; these four pairs are in the fill list and in the test. The difference between the
   normal and the pressed fill (1.26 / 1.37) is a **supplementary** cue, deliberately outside the A1.10 item 2
   assertion; the press has no other state cue, and "every pair" stays true because the asserted pairs are the ones
   listed.
5. **Rule:** a chip sits on one of the four surfaces above, never on another tint; links are the text colour,
   so no accent-on-surface link pair exists.

Not covered by a ratio: hairlines (decorative); the state is always also in text.

### A1.3 Layout (a top bar, no sidebar: A1.13)

```
┌ Cirak   Sessions  Executions  Catalog and archived  Audit  About and health ───── bridge-a  Token expires in 52 min  [Theme]  Read-only ┐
├────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────┤
│  main content, centred, at most 96rem wide, 16 px gutters, cards 12 px radius, tables before cards                                      │
└────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────┘
```

- **Top bar** (48 px, not sticky): the "Cirak" wordmark (text, no image); the five destinations as pills
  (36 px, radius 18, `aria-current="page"` fills the current one with the primary colour); an instance label (Q3);
  the connector-token expiry badge in remote mode (A1.8; absent in local mode, where ADR-019 says there is no
  expiry; neutral, warn under 60 minutes, error once expired); the theme toggle (hidden unless JS runs); the word
  "Read-only". **The instance label and the token-expiry badge arrive in S3 with `Source.Health` and the label
  variable (A1.9); the S1 and S2 top bars omit them, by the same rule as a destination.**
- **Below 60rem** the destinations collapse into a `<details>` menu below the wordmark (open without JS, closed by
  the script on narrow screens, JS-6); the content is one column at 320 px (WCAG 1.4.10); tables scroll inside
  their own wrapper, never the page.
- A destination appears only when its page exists (no dead links in S1).

### A1.4 Screens

All screens: a `<main>` landmark, one `<h1>`, tables with a visually hidden `<caption>` and `th scope`,
absolute time in `title` and in the detail view's text beside the relative form (`<time datetime>`), IDs and
paths in mono, times and durations in tabular numerals, labels in sentence case. State lives in the URL (A1.13).

1. **Sessions (`GET /`).** Toolbar (the low surface): search, state filter, harness filter, the archived link
   (`?archived=1`), and the **Refresh link** (the no-JS refresh of A1.6 JS-3 and the page's primary action). Sticky-header table, columns: **harness** (a badge with the harness name: D17 Q8 keeps the distinct harness
   badge and column), **title and ID**, **repository and path** (mono; a path longer than 40 runes is shown as an
   ellipsis plus its **last two segments**, never cut inside a segment; two rows of one list that would then render
   identically get a stable 6-hex suffix taken from the sha256 of the full path; the full value is in `title`, on the
   detail page and in the copy control), **lifecycle root**, **state chip**,
   **guard**, **current execution**, **last activity** (relative, absolute in `title`), an **Archived** chip.
   Caps unchanged: the page renders the first 200 rows and says "Showing the first 200 of N sessions" (`maxListRows`,
   `internal/dashboard/poll.go:96-98`). Tables before cards: the table is the screen.
2. **Session detail (`GET /session/<harness>/<id>`).** The signature screen (A1.13):
   - A header card: copyable ID, path, lifecycle root, state chip, guard (a key/value list).
   - **The execution timeline**, one table, newest first: state chip, execution, start, duration, and a liveness
     cell. Under the **current** execution a detail row carries the **liveness report (ADR-017)**: native status
     chip, retry attempt and next attempt time, last native progress (relative and absolute), observed-at, the
     stall reason as a chip with its plain-language sentence, and the fixed note "This is a report, not a
     verdict; the Bridge takes no action." The dashboard keys on `stall_reason`, **not** on `stall_suspected`
     alone (review #459 P2-1). Values of ADR-017 A2 (`PROVIDER_RETRYING`, `PROVIDER_ERROR`, `TOOL_RUNNING_LONG`,
     `HARNESS_UNREACHABLE`, `cause_hint`, `tool`, `thresholds`) are **reserved rows in the label map, rendered only
     after A2 is accepted and its slice ships**; A1 depends on A1 fields only.
   - **Pending permission** is a secondary-tinted row of the same table: "Awaiting a human", the count of pending
     native permissions and, only when the Bridge attributes one to an execution (ADR-024), the fields ADR-024
     already exposes to Chat (tool, kind, pattern count, digest). Never a pattern, never native text. With the
     default incarnation mode off it says it is not attributed and to answer it in OpenCode.
   - **Changes in the working tree:** the totals, the **redaction count**, a file list with status word and
     `+N -M` counts (text, not colour only), per file a `<details>` (the first open, the rest collapsed; no border
     box, hairline separators), the diff in `<pre>` as text (unified; a split view is Q7), added and removed lines
     with a tint **and** the `+`/`-` character; per-turn diffs behind the existing turn links; the "diff
     unavailable" state with its reason (Claude Code history: ADR-021 pending, unchanged).
3. **Executions (`GET /executions`).** A flat table, newest first, of **only the executions of sessions in the
   scope set of the Sessions list**: the same ACL-authorized scope (D17 Q6), and the executions of an **archived**
   session only with `?archived=1` (D17 Q7, ADR-025), through the same request-context flag the sessions list uses.
   **The join set is that scope set before the Sessions page's 200-row display cap** (the page truncates what it
   shows, not what is in scope), so an execution of the 201st session can appear here; a footnote says so
   ("includes sessions beyond the first 200 shown on the Sessions page"), as it says that a restart empties the list
   (ADR-010) and that archived sessions' executions are behind the archived filter. The **cap of 200 rows is
   applied to the executions after the filter**, so a hidden session can neither appear nor crowd a visible one out.
   The **Session** column and its link are built from the harness-qualified session ID carried on the row (A1.8). Data:
   A1.8.
4. **Catalog and archived (`GET /catalog`).** Sessions grouped by lifecycle root (the existing label) with the
   archived filter; read-only; it widens nothing (ADR-022 §6.1).
5. **Audit (`GET /audit`, `?page=N`).** The bounded ring in pages of 100 (a server-side page parameter, GET,
   bounded); the **event** as a neutral chip, the **outcome** as a chip from a fixed label map, the error code,
   session, execution, principal and the "other" attributes as today; the client-side filter stays. **The ring is the
   D17 Q4 process audit and is not ACL-filtered** (A1.0): an entry can name a session that is not in the Sessions list.
6. **About and health (`GET /health`).** A key/value list: version, build (the VCS revision when the binary
   carries one, else unknown), connector token expiry (remote mode only), lifecycle, permission relay and Claude
   Code as On/Off chips (one row each, no joined strings), harness reachability **taken from the outcome of the last
   session-list read** (no extra native probe), audit feed on/off and fill.
7. **States.** Every data region has the four states of A1.13: loading, empty, error and full, with the
   server-rendered "loading" marked not applicable; a stale banner when a poll fails; *503* (page text plus
   `Retry-After`, as §4); *404* (fixed text, never echoes the identifier). Each has a heading, a sentence and a link
   back or the one action that fills it.

### A1.5 Accessibility (WCAG 2.2 AA)

- **Contrast:** A1.2, both themes, as a test. **Not colour alone (1.4.1):** every chip has its word.
  **Non-text contrast (1.4.11):** input border, focus ring and outline chip, all >= 3:1 (A1.2).
- **Keyboard:** every control reachable in DOM order; a visible 2 px focus ring on every focusable element,
  appearing instantly; a "Skip to content" link first; the top bar is not sticky, so a focused element is never
  hidden behind it (2.4.11).
- **Target size (2.5.8 and Hallmark's hit target):** controls 36 px visual, 44 px minimum for coarse pointers;
  chips are not interactive.
- **Structure:** `lang`, a meta description, landmarks (`header`, `nav` with a label, `main`), heading order,
  table captions and `th scope`, `aria-current`, `<time datetime>`; no information only in `title`.
- **Reflow and spacing:** usable at 320 px width and 200% zoom (1.4.10, 1.4.4); text-bearing elements (chips,
  badges, inputs, buttons) use `min-height`, never a fixed `height`, so the text-spacing overrides do not clip
  (1.4.12; the only fixed height is the visually hidden element).
- **Scroll wrappers:** every table wrapper is a keyboard-reachable named region (`tabindex="0"`, `role="region"`,
  `aria-labelledby` the screen heading), so a scrollable table can be read without a pointer. The sticky table head
  has `scroll-padding-top` on its wrapper so a focused row is not hidden under it (2.4.11), and the wrapper's
  `max-height` has a floor (`max(calc(100vh - 14rem), 12rem)`) so a short viewport does not collapse it.
- **Motion:** `prefers-reduced-motion: reduce` disables every animation and transition. `forced-colors: active`
  gives chips a `CanvasText` border.
- Not claimed without a real render and a screen-reader pass (UNVERIFIED, A1.11); the three wrapper and
  text-spacing rules above are tests in A1.10 that need no render for their static half.

### A1.6 CSP, security and JavaScript

**CSS.** The stylesheet is the one embedded `/dashboard.css` served under `style-src 'self'`; the pages carry
**no inline `<style>` and no `style=""` attribute**, so the CSP needs no hash and no nonce. A nonce would
need per-request state and would defeat the ETag/304 and the 1 s view cache of §4; a hash would need a build
step. **This departs from the task text ("inline CSS with a hash/nonce") in favour of a strictly tighter
policy; D31 confirmed it (Q9).** Proportions use classes or SVG presentation attributes. Icons, if any, are
inline `<svg aria-hidden="true">` with fixed paths (none in v2: chips carry words); no `url()` to a remote host,
no `@import`, no `@font-face`.

**JavaScript inventory** (the embedded scripts of A1.17 (D34): a shared core `dashboard.js` and one script per page kind, each <= 16 KiB and all of them <= 32 KiB, plus `theme.js`, <= 1 KiB; the
existing rules stay: values set with `textContent`, no markup built from data, nothing evaluated, `GET` only):

| # | Behaviour | CSP impact | No-JS fallback |
| --- | --- | --- | --- |
| JS-1 | Copy-ID button (`navigator.clipboard.writeText`) | none (`script-src 'self'`) | the ID is selectable text (`user-select: all`); the button is `hidden` until the script runs |
| JS-2 | Theme toggle; `theme.js` is a blocking `<script src>` in `<head>` that sets `data-theme` from `localStorage` before first paint. **The stored value is checked against the two allowed words (`dark`, `light`) before it is used; anything else is ignored and removed.** The storage key is `cirak-theme`. | none; a second same-origin file | the system preference through `prefers-color-scheme`; the toggle stays hidden; a blocked `localStorage` is caught and ignored (Q1). The dark state is explicit too: `:root[data-theme="dark"]` sets `color-scheme: dark`, as the light blocks set `light`, so the native controls follow the toggled theme |
| JS-3 | Poll refresh of `/api/state` (existing), pausing when hidden (A1.7) | none (`connect-src 'self'`) | a "Refresh" link; optional `?refresh=N` (Q8) |
| JS-4 | Client-side filter and search for the sessions, executions and audit tables; **the filter state is written to the URL query with `history.replaceState` and re-applied on load.** Query values reach the page only through `.value` of a control or `textContent`, the state and harness values are checked against the options the control offers, and nothing from the query is ever parsed as markup or URL. The filter text stays in the browser history (A1.11); `Referrer-Policy: no-referrer` keeps it out of any request. | none | the full table is shown; the query is ignored |
| JS-5 | Re-render `<time>` relative text from `datetime` every 30 s; no network | none | the server-rendered relative text and the absolute time in `title` |
| JS-6 | Close the navigation `<details>` below 60rem and open it above (`matchMedia`) | none | the menu is open: every destination is visible |

Forbidden by test in the shipped JS: `innerHTML`, `outerHTML`, `insertAdjacentHTML`, `document.write`, `eval`,
`new Function`, string arguments to `setTimeout`/`setInterval`, dynamic `import()`, any `http:`/`https:` literal,
`localStorage` for anything but the theme key. No third-party code.

**Security checks that stay or are added:** escaping fixtures (T1) for every new template field (titles, paths,
labels, tool names); response headers on every response including errors; no CORS headers; Host/Origin as
today; the method sweep returns 405 on every route including the new ones; no new native read (A1.8).

### A1.7 Auto-refresh (amends §4's hidden-tab cadence)

- Visible: the list polls every 5 s, the detail every 2 s (as §4), `If-None-Match`, `304` when unchanged.
- **Hidden tab: paused** (no timer, no request, the in-flight request aborted) and **one immediate refresh
  when the tab becomes visible again**. §4's "every 10 s when hidden" is superseded.
- Errors: the retry delay after a failure is `max(the page's own interval, Retry-After)`, doubled on each further
  failure up to 60 s, and reset on success. **The floor is the page's own interval (5 s for the list, 2 s for the
  detail), never lower**: a failed poll never retries faster than a healthy one, and a small `Retry-After` cannot
  speed it up (a `503` from the request limiter must not draw more load). A failed poll shows the stale
  banner ("Showing the data from HH:MM:SSZ; the last refresh failed") and keeps the last data.
- No-JS: no automatic refresh by default; a visible "Refresh" link; an opt-in `?refresh=N` meta refresh
  (10 <= N <= 300, validated server-side, an invalid value ignored) is Q8.
- **The new pages use the same machinery as `/api/state`:** the shared limiter (4 concurrent, 8 in flight, `503` with
  `Retry-After` beyond), `If-None-Match`/`304`, the 1 s server-side view-model cache (its key carries the archived
  flag and, for the audit, the page), and the 5 s context bound. Each new `Source` reader counts against that one
  limiter. Intervals while visible (all paused while hidden): `/executions` 5 s, `/catalog` 5 s, `/health` 30 s,
  `/audit` page 1 only, 10 s (page 2 and later do not poll: the ring is a log, not a live view).

### A1.8 Data and Source additions (all read-only, all from memory the Bridge already holds)

| Addition | Source | Notes |
| --- | --- | --- |
| `Execution.Liveness` (the ADR-017 A1 fields) and a pending-permission summary on the dashboard `Execution` | the existing `execution.Execution` copies in `DashboardSnapshot()` (`internal/execution/dashboard.go`), mapped in `internal/mcp/dashsource.go` | no native read; same copy-under-`m.mu`-then-each-`x.mu` order (T10); permission summary = what ADR-024 already shows Chat, never a pattern |
| `Source.Executions(ctx)` | `DashboardSnapshot().Executions` (which holds **every** execution of the Manager and applies no cap, `internal/execution/dashboard.go:33-36`), **filtered in the Source implementation by the scope set the same call chain produces** (the ACL scope of D17 Q6, the archived flag of D17 Q7, joined on the harness-qualified session ID), **before** the Sessions page's 200-row display cap, then sorted newest first, then capped at 200 | the filter comes **before** the executions cap; an execution whose session is not in the scope set is dropped, never shown; the page's JSON twin calls this same reader, never a second implementation; the T11 method-set pin test lists the new reader |
| the dashboard `Execution` row type (`internal/dashboard/source.go:22-30` today: `ID`, `State`, `Started`, `Updated`, `Ended`, `Current`) | S3 adds the **harness-qualified session ID** (`opencode:ses_…` today, copied from `execution.Execution.SessionID`; the dashboard source takes executions from the OpenCode `Manager`, whose IDs are `opencode:`, `internal/execution/manager.go:408`; a `claudecode:…` ID only appears once Claude executions are wired into the dashboard source, ADR-021 wiring, and is not supplied today) to the row; the Session column shows the session ID and links to `/session/<harness>/<id>` built by splitting the qualified ID at the first colon | the field is display data; the T11 type test pins it; an ID the sessions list does not know renders without a link |
| `Source.Health(ctx)` | version and build from the binary, features from configuration, connector token **expiry timestamp** (never the token, never the Host or origin) from the remote settings through a read-only getter, harness reachability from the last list read | no new native probe; absent fields are omitted, never defaulted |
| instance label | optional **`CIRAK_DASHBOARD_LABEL`** (ADR-026 §2 renames the environment prefix `BRIDGE_*` to `CIRAK_*`; ADR-026 and ADR-027 are still PROPOSED, so this is a cross-reference, not a decision: Q16), <= 40 bytes, `[A-Za-z0-9 ._-]`, default none | startup error on an invalid value, like the redaction variable (Q3); ADR-027's config schema would carry the same key |
| audit `?page=N` | the existing ring, pages of 100 | bounded GET parameter; an invalid value is page 1 |
| routes | `GET/HEAD` only: `/executions`, `/catalog`, `/health`, and the JSON twins `/api/state/executions`, `/api/state/catalog`, `/api/state/health` (`/api/audit` already exists) | each is listed in the route table; the 405 sweep and the Host/Origin checks cover them |

The dashboard package still depends on the `Source` interface only; the T11 test lists the exact reader set.

### A1.9 Slices (one PR each; the amendment is ACCEPTED, so no slice needs to record the acceptance)

- **S1 — tokens, shell, sessions.** `assets/dashboard.css` from the reference file (dark only; `color-scheme:
  dark`), the new page shell (top bar with the destinations that exist, the menu below 60rem, JS-6), the sessions
  table with chips and the label map, path shortening, relative time, filters with URL state (JS-4), the A1.7
  pause-on-hidden behaviour, golden snapshots, the CSP / no-inline / no-external-URL / typography tests, the
  contrast test over the dark tokens, A1.0 route and method sweeps.
- **S2 — detail.** `Execution.Liveness` and the permission summary in `Source`, the detail page (header, the
  execution timeline with its liveness and permission rows, the diff with file list and `<details>`), JS-1,
  per-page snapshots with fixtures for every liveness reason, hostile-input fixtures.
- **S3 — audit, health, executions, catalog, light theme.** The audit restyle and `?page=N`, `Source.Health` and
  `/health`, **the top bar's token-expiry badge and instance label (`CIRAK_DASHBOARD_LABEL`, with the read-only
  expiry getter)**, `Source.Executions` and `/executions` (scoped to the scope set, A1.4 item 3, with the session
  field on the row and the A1.10 scope tests), `/catalog`, the light token blocks, `theme.js` and the toggle
  (JS-2), the light contrast checks and snapshots. S3 may be split into S3a (pages) and S3b (light theme) by the
  implementer.

Each slice is RED-first with the A1.10 cases it owns. No slice needs a live service; the visual result is
confirmed by the operator in R-DASH (below).

### A1.10 Test plan (RED-first)

1. **Golden HTML snapshots** of every screen and state with a fixed clock and fixture source, committed under
   `internal/dashboard/testdata/`, one per theme where it applies.
2. **Contrast as code:** parse the custom properties of the embedded CSS, composite the 0.14 tints, compute the
   WCAG ratio, assert every pair of A1.2 (>= 4.5 text, >= 3 non-text) for each theme present, **including the
   states a component draws (the pressed primary fill)**; the test fails when a token changes.
3. **CSP header test:** the exact string of §3 on every response; **no `<style>`, no `style=`, no `on*=`
   attribute, no inline `<script>`** in any rendered page (all screens, all states).
4. **No external URL:** the templates, CSS and JS **code** (comments stripped before the scan, or the embedded
   copy has its comments removed in S1; the reference file's comments may name a project) contain no
   `http:`/`https:`/protocol-relative URL, no `@import`, no `@font-face`, no `url(` to a non-embedded path.
5. **JS hygiene:** the forbidden-API scan of A1.6 on **every** script; a size cap on each script, on their total (A1.17) and on `theme.js`.
6. **Reserved colour:** no class other than the pending ones refers to the secondary tokens.
7. **Typography (Hallmark Floor 9):** the CSS contains no `text-transform: uppercase|capitalize`, no
   `letter-spacing` on a label, table head, tag or nav link, no `font-variant: small-caps`; no template renders a
   raw snake-case state word as a chip label (the label map is total over the known values).
8. **Floor 1, 2 and 4:** no literal colour or `font-family` outside the token blocks, no `transition: all`, no
   animation of `width`/`height`/`top`/`left`, `overflow-x: clip` on `html` and `body`, no `100vw`.
9. **No-JS render:** the same pages with the script blocked show the full tables, the open menu, the Refresh
   link and no dead control (the toggle and copy button stay hidden).
10. **Accessibility structure:** `lang`, a meta description, landmarks, one `h1`, `caption` and `th scope` on
    every table, `aria-current` on exactly one nav link, labels on every input, no skipped heading level.
11. **Security and scope:** XSS fixtures in titles, paths, labels, tool names and audit values for every new
    template field **and an XSS fixture for the query string** (`?q=`, `?state=`, `?harness=`: reflected only as
    escaped text or ignored); **an execution of an archived session is absent from `/executions` without
    `?archived=1` and present with it; an execution of a session outside the ACL scope is never listed, with or
    without the flag; the cap applies after the filter** (a hidden session's executions never push a visible one
    out); `/executions` never lists an execution whose session is not in the scope set (a subset property test);
    **a fixture with more than 200 in-scope sessions: an execution of the 201st session is listed on `/executions`
    (the join set is the scope set before the display cap) while the Sessions page shows 200 rows and the truncation
    note; the JSON twin `/api/state/executions` goes through the same `Source.Executions` and passes the same
    archived, out-of-scope, cap-after-filter and subset assertions; a request with `?archived=1` followed within
    1 s by one without never serves the first response (the cache key carries the flag), for the page and the
    twin;** the audit page is out of these tests (A1.0); route table lists only GET/HEAD; every route answers 405 to POST/PUT/PATCH/DELETE; T10 and T11 tests
    updated for the new readers; the dashboard still produces no AUDIT-01 mutation line.
12. **Refresh and URL state:** a fake clock test that no request is sent while hidden, one on becoming visible,
    back-off and `Retry-After` bounds **with the floor at the page's own interval** (a first failure never retries
    sooner than 5 s on the list, and `Retry-After: 1` does not shorten it); the filter state round-trips through the
    URL query; the stored theme value is ignored unless it is `dark` or `light`; the new routes use the shared
    limiter, ETag and cache.
13. **Accessibility rules in the CSS and the templates (static half; the render half is UNVERIFIED until R-DASH):**
    no `height` declaration on chips, badges, inputs or buttons (`min-height` only; the visually hidden element is
    the one exception); every table wrapper has `tabindex="0"`, `role="region"` and an accessible name; the wrapper
    has `scroll-padding-top` and a `max-height` with a floor.
14. **Race:** `go test -race -count=20 ./internal/dashboard ./internal/mcp`.

Mutations (each must be killed): the primary pressed fill reverted to `--hairline`; `/executions` lists the
executions of an archived session without the flag; the cap applied before the filter; an execution of an
out-of-scope session listed; the JSON twin served from an unfiltered list; the cache key without the archived flag
(a stale-flag response); the join set cut at 200 sessions; the CSP string weakened; an inline `style=` emitted; the dark error chip colour
reverted to `#e57373`; a chip label rendered in capitals; `letter-spacing` added to a table head; a raw state
word shown as a label; the secondary colour used for a non-pending state; polling continued while hidden;
`template.HTML` used for a title; a remote `url()` in the CSS; `th scope` or `lang` dropped; the liveness row keyed
on `stall_suspected` alone; an unknown liveness reason rendered as if known.

### A1.11 Limitations / UNVERIFIED

- The design values are the maintainer's token list as given in tasks #463 and #469; the Lokman source was not
  read. The "A1" values (light backgrounds, text and muted colours, hairlines, error chip text, control height,
  spacing and type scales) are chosen here.
- Contrast is computed with the WCAG 2.x relative-luminance formula (not APCA) on the CSS file; browsers were
  **not** used. The static mock was checked for well-formedness, duplicate IDs, dangling anchors, unstyled
  classes and forbidden constructs by a script and linted with Hallmark's `sloplint` (A1.13), but **was not
  rendered in any browser** (none is installed on the host, and `sloplint --render` was not run): layout, sticky
  headers, the `<details>` menu, `rgb(r g b / a)` colour syntax support and the 44 px coarse-pointer rule are
  UNVERIFIED until S1 is viewed in R-DASH.
- Keyboard, screen-reader and zoom behaviour are design claims, not test results.
- The static mock is one page with **seven `<h1>`** (one per screen, as its header comment says; each real screen is
  its own page). The A1.10 tests that need one page per document (one `h1`, landmarks, the no-JS render) run on the
  real per-page templates, not on the mock; the mock is checked by script for tag balance, IDs, anchors, styled
  classes and forbidden constructs only.
- The filter text written to the URL stays in the browser history of the operator's browser (local, and kept out of
  requests by `no-referrer`); it is search text, never a credential.
- System fonts render differently per OS; relative-time and path-shortening text depends on the clock and the
  fixture.
- ADR-017 A2 is PROPOSED with open review findings (#459): A1 renders A1 fields only and keys on `stall_reason`.
- The sources and native shapes of §1 remain as stated there; nothing here adds a native read.

### A1.12 Decided (D31, 2026-11-21)

The maintainer answered all sixteen questions of this amendment with **the recommendation** in decision D31
(2026-11-21, recorded in issue #117). Each question keeps its text; the decision follows it.

1. **Light theme.** Media query plus an optional toggle stored in `localStorage` (JS-2), or media query only?
   Recommended: both (the toggle is progressive; without JS the system preference applies).
   **Decided (D31): media query plus an optional JS toggle.**
2. **Fonts.** System stack only (recommended), or embed Roboto under its OFL licence (about 100-150 KB per
   weight, a licence notice to ship)? **Decided (D31): system fonts.**
3. **Instance label.** A new optional `CIRAK_DASHBOARD_LABEL` (recommended, default none), or show the host name?
   **Decided (D31): `CIRAK_DASHBOARD_LABEL`, default none.** (The question text named `BRIDGE_DASHBOARD_LABEL`, the
   name before ADR-026's prefix; D31 names `CIRAK_DASHBOARD_LABEL` and Q16 already recommended it, so the text now
   carries D31's name; see A1.16.)
4. **Token-expiry badge.** Fed by a read-only getter of the connector token's expiry (remote mode only, never the
   token), recommended; or drop the badge from v2? **Decided (D31): the badge, via a read-only getter.**
5. **Executions page.** Add the `Source.Executions` reader (recommended; the data is already in
   `DashboardSnapshot`), or keep executions inside the session detail only? **Decided (D31): the Executions page.**
6. **Error-chip text colours.** Approve the A1 deviation (`#ef9a9a` dark, `#b71c1c` light) and the A1 light
   surfaces, or provide other values? **Recommended: approve**; the extracted values fail AA on three surfaces (A1.2).
   **Decided (D31): the A1 error-chip colours and the A1 light surfaces.**
7. **Diff view.** Unified only in S2 (recommended), with a split view as a later slice (a `?diff=split` GET link,
   no JS)? **Decided (D31): unified diff first, split later.**
8. **No-JS auto refresh.** Offer the opt-in `?refresh=N` meta refresh (recommended off by default), or only the
   Refresh link? **Decided (D31): the opt-in `?refresh=N`.**
9. **CSS delivery.** Confirm the embedded stylesheet under `style-src 'self'` (no inline CSS, no hash, no nonce)
   instead of the task's "inline CSS with a hash/nonce". **Recommended: confirm the embedded stylesheet** (stricter,
   and it keeps the ETag and the 1 s cache). **Decided (D31): the embedded stylesheet under `style-src 'self'`.**
10. **Source of truth.** Keep `docs/design/dashboard-v2.css` as the reference and copy it in S1 (recommended), or
    treat `assets/dashboard.css` as the only copy once S1 merges and delete the design file then?
    **Decided (D31): `docs/design/dashboard-v2.css` is the reference, copied in S1.**
11. **Wordmark and name.** "Cirak" as text only in the top bar and in each page `<title>` suffix; is a logo
    needed (an inline SVG would be allowed by the CSP)? **Recommended: text only** in v2; no logo asset.
    **Decided (D31): text wordmark only.**
12. **Relative time.** Relative text with the absolute time in `title` (plus the absolute time in the detail
    view), or absolute only? **Recommended: relative text with the absolute time in `title` and in the detail view.**
    **Decided (D31): relative time with the absolute time in `title` and in the detail view.**
13. **Menu behaviour.** A `<details>` menu closed below 60rem by a six-line script (recommended; open and usable
    without JS), or a CSS-only pattern that needs newer browser features (`::details-content`)?
    **Decided (D31): the six-line `<details>` script.**
14. **Hallmark in the loop.** Keep `sloplint --scope app` as a manual gate run by the author of each slice
    (recommended; it needs Node and is not a repository dependency), or add it to CI?
    **Decided (D31): `sloplint --scope app` as a manual per-slice author gate.**
15. **Label maps.** Approve the sentence-case labels of A1.1 (state, liveness reason, audit event and outcome) as
    the display vocabulary, with the raw word in `title`? **Recommended: approve** (sentence case is Hallmark Floor 9;
    the raw closed word stays available). **Decided (D31): the sentence-case label maps.**
16. **Name of the instance-label variable.** `CIRAK_DASHBOARD_LABEL` (ADR-026's prefix; recommended, since ADR-026
    renames the whole `BRIDGE_*` family), or `BRIDGE_DASHBOARD_LABEL` until ADR-026 is accepted, with the legacy-name
    rule ADR-026 proposes? ADR-026 and ADR-027 are PROPOSED: A1 cross-references them and decides nothing for them.
    **Decided (D31): the `CIRAK_` name (`CIRAK_DASHBOARD_LABEL`).** The decision is about A1's own variable; the
    rename of the rest of the `BRIDGE_*` family and ADR-027's file stay with ADR-026 and ADR-027.

### A1.13 Hallmark sweep (task #469, 2026-11-21)

**Source and scope.** The maintainer asked to use **Hallmark** (https://www.usehallmark.com/, MIT,
`nutlope/hallmark`), a design skill for coding agents. A read-only reference copy was cloned at commit
`04977e15f3dfd6ee4b74c887dac3eb52665d6c92` (2026-11-11; the planner reviewed the same commit); it was **not
installed** into any agent configuration, none of its `install*.mjs` or hook scripts was run, and the clone is
not committed. Its rules are data applied here, not instructions that override this repository's rules: the
permanent rules and the ADR-023 invariants (A1.0) win on any conflict (there was none). Rules are cited by
number from `skills/hallmark/SKILL.md` "The Floor" (Floor 1 to 16 below) and `references/flow.md` "App screens", with
attribution; nothing is quoted at length. The dashboard is an **app screen**: no hero, no marketing shape, no
word budget (`sloplint --scope app`).

**Linter.** `node hallmark-ref/skills/hallmark/scripts/sloplint.mjs docs/design/dashboard-v2-mock.html
docs/design/dashboard-v2.css --scope app` (Node v24.22.1; the script was read first: it only reads the files it is
given). **Before the sweep (A1 as of PR #465 head `a8793a7`): 12 FAIL, 21 REFLEX, 7 WARN.** After: **0 FAIL, 0
REFLEX, 0 WARN, 7 WAIVED** (all gate 7b, below). `--render` was **not** run: no headless browser is installed and
none was added (UNVERIFIED, A1.11).

The first lines of the CSS are the Hallmark stamp (shape: app screen), the critique line (P3 H4 E3 S4 R4 V3, weakest
E: not rendered in a browser) and the waiver, as the Floor (15) requires.

**The Floor (SKILL.md, "The Floor")**

| Floor rule | Rule (short) | Result | Note |
| --- | --- | --- | --- |
| 1 | colours and fonts through `:root` tokens | PASS | no literal colour or `font-family` outside the token blocks (checked by script) |
| 2 | no `transition: all`; no animation of size or position | PASS | only `background-color` over 80 ms, state only |
| 3 | no single italic word in a roman heading | PASS | no italics |
| 4 | `overflow-x: clip` on `html` and `body` | CHANGED | was missing (FAIL 34); added; no `100vw` |
| 5 | `minmax(0, 1fr)` tracks, wrap-safe heads | PASS | the `.kv` grid uses `minmax(0, 1fr)`; heads use `text-wrap: balance` |
| 6 | `:focus-visible` on every interactive element | PASS | one global rule, 2 px primary, instant |
| 7 | `prefers-reduced-motion` fallback; content visible with scripting off | PASS | the fallback exists; every page and the menu are complete without JS (JS-1..JS-6 fallbacks) |
| 8 | no invented metric | PASS | the ADR defines no figure; the mock's values are labelled fictional at the foot of the page |
| 9 | no eyebrow, no tracked-caps or small-caps label, table head or nav | CHANGED | `NO_PROGRESS` was typed in capitals in a chip (FAIL 67), and an eyebrow line sat above the first screen (FAIL 54): chips now show sentence-case labels from a map (A1.1), the sample-data note is a footer line |
| 10 | no middle-dot meta strings, glued arrows, decorative status dots | CHANGED | two middle-dot strings (FAIL 63) became one key/value row each; chip glyphs removed |
| 11 | no purple-to-blue gradient, card in card, emoji icon, fake window | CHANGED | the geometric glyphs in chips were read as emoji icons (FAIL 30a, six places): removed, the label is the cue; the bordered `<details>` boxes inside a card became hairline-separated rows |
| 12, 13, 14 | hero fold, about 80 words per screen, capped container | N/A | the hero and the word budget are skipped in the app scope; the container is capped and centred at 96rem anyway |
| 15 | the stamp is the first CSS line | CHANGED | added (FAIL 20) |
| 16 | run sloplint (`--render` when a browser exists) | PASS, static run only | 0 FAIL after the sweep, counts above; `--render` **not run** (no headless browser, none installed) |

**App-screen rules (`references/flow.md`, "App screens")**

| Rule | Result | What was decided and why |
| --- | --- | --- |
| Shell: a top bar by default; a sidebar only above about seven destinations; a menu under 60rem | **CHANGED** | A1 has five destinations, so the 248 px left nav is gone (A1.3). Below 60rem the destinations collapse into a `<details>` menu (JS-6; open without JS). |
| Type scale fixed at a ratio of 1.125 to 1.2; system face allowed; tabular numerals | **CHANGED** | one scale at about 1.17 (12, 14, 16.4, 19.2 px) on tokens; `font-variant-numeric: tabular-nums` on times, durations and counts |
| Colour restraint: accent only on the primary action, the current selection and state; keep the reserved secondary | **CHANGED** | links are the text colour, underlined; primary is the current nav item, the primary button, the focus ring. The reserved secondary rule is kept and tested (A1.1, A1.10). |
| Density: controls 32-40 px with a 44 px hit target; tables before cards | **CHANGED, reconciled** | **Hallmark's range wins over A1's 30 px:** 36 px visual height (inside 32-40, and above WCAG 2.5.8's 24 px) keeps the compact table density; the **44 px hit target applies to coarse pointers** (`@media (pointer: coarse)`), where a bigger control costs nothing. Detail data moved from four cards to a header card, one timeline table and one changes list. |
| Four states per data region | **CHANGED** | table below |
| State in the URL: filters, archived, page | **CHANGED** | `archived` was already in the URL; the filter state is now synced to the query by JS-4 (the server does no filtering, ADR-023 §5.1 stays); the audit ring gets `?page=N` (A1.8) |
| Motion is state only | PASS | 80 ms and 140 ms colour changes on hover and press; nothing animates on load |
| The signature | **NAMED** | **the per-session execution timeline with its liveness report and stall reason**: the detail page is built around it (A1.4). |

**Four states per data region** (loading is **N/A** for a server-rendered first paint; during a poll the last data stays and a failed poll shows the stale banner):

| Region | Loading | Empty | Error | Full |
| --- | --- | --- | --- | --- |
| Sessions table | N/A | "No sessions" with the archived link | 503 page with `Retry-After`; stale banner on a failed poll | the table |
| Session header | N/A | not applicable (a session exists or 404) | "This view cannot be read" / 404 | key/value list |
| Execution timeline | N/A | "No executions" for this process | error state; stale banner | rows with the liveness report |
| Liveness report | N/A | absent before submission and on terminal executions: the cell says "Ended" or is empty, never a guess | "Unavailable" with the reason class | report row |
| Pending permission | N/A | no row when none is pending | not attributed: says so | secondary row |
| Changes (diff) | N/A | "No changes" | "Diff unavailable" with the reason (snapshot disabled, binary, truncated, history pending) | file list and diffs |
| Executions page | N/A | "No executions" (for the sessions the list shows) | 503 / error | the table, after the scope filter |
| Catalog and archived | N/A | "No sessions in this root" | error | grouped table |
| Audit | N/A | "The audit feed is not enabled" / no entries | error | the page of 100 |
| About and health | N/A | not applicable | unreachable harness shown as a chip | key/value list |

**Waiver (gate 7b, Reflex).** `#ffffff` paper and text on the primary colour are pure white. **Waived on the record**
in the CSS stamp: the stamp declares `genre: modern-minimal`, which allows `#fff` paper (Hallmark gate 7b); the light
paper is the maintainer's own token (D28) and the light text on `#2962ff` needs white to reach 4.90:1. All other
Reflex findings were fixed, not waived: off-scale spacing (gate 24b), missing `:active` and `:disabled` states
(26b), the accented word in a heading (38iii), side stripes (3: replaced by hairline borders all round), two sticky
`top:0` elements (FAIL 56: the top bar is no longer sticky; the table header sticks inside its own scroll
wrapper), and three Finish-list warnings (balanced heads, typographic quotes, the missing meta description).

**What did not change.** Everything in A1.0; the data model; the routes except the additive ones of A1.8; the
reserved secondary rule; the contrast result (now 124 token pairs and 26 fill pairs, 0 failures, minimum text 4.62:1,
minimum non-text 4.06:1; the two pairs added in A1.15 are text on the pressed primary fill, the four added in A1.16
are the pressed fill against its surfaces).

### A1.14 Review #476 dispositions (task #477, 2026-11-21)

Review #476 (Worker C, second review of PR #465 at `ab69132`): PASS-WITH-NITS, one P2 and nine P3. Every cited
code line was re-checked on main `cdaf5bd` and held. No finding was false.

**P2-1, before and after.** *Before* (A1.4 item 3): "A flat table of the executions the Bridge holds, newest first,
capped" and (A1.8) "`DashboardSnapshot().Executions`, newest first, capped"; the Manager's snapshot holds **every**
execution and applies no cap (`internal/execution/dashboard.go:33-36`), so an archived or out-of-scope session's
executions would have been listed. *After:* `/executions` shows only the executions of the sessions the Sessions
list shows (ACL scope, D17 Q6), an archived session's only with `?archived=1` (D17 Q7, ADR-025), the cap (200) is
applied **after** the filter; the filter lives in the `Source` implementation by joining on the harness-qualified
session ID; two tests and a subset property are added (A1.10 item 11) and the matching mutations (A1.10). D17 is
conformed to, not reopened.

| Finding | Disposition | Where |
| --- | --- | --- |
| **P2-1** executions ignore D17 Q6/Q7 | **FIXED** | A1.0 (scope row), A1.4 item 3, A1.8, A1.9 S3, A1.10 item 11 and mutations |
| P3-1 test 4 would fail on the reference CSS comments | **FIXED** twice: the scan is on code with comments stripped (A1.10 item 4), and the two comments no longer contain a URL or the `@import` text (`dashboard-v2.css:11,14`) | A1.10, the CSS |
| P3-2 contrast numbers | **FIXED**: exact (unrounded) compositing; light error chip on white **4.498** (it fails, not "4.501"), 4.32 and 4.05 on the light surfaces; the nested tint 3.96; minimum **text 4.62**, minimum **non-text 4.06**; the count is **122 token pairs + 22 listed fill pairs** (the earlier "216" counted the light theme twice, once for the media-query block and once for the identical toggle block, over a smaller pair set; the reproducible figures replace it) | A1.2, A1.13 |
| P3-3 error back-off floor | **FIXED**: the floor is the page's own interval (5 s list, 2 s detail) | A1.7, A1.10 item 12 |
| P3-4 polling and limits of the new pages | **FIXED**: same limiter, ETag, 1 s cache and 5 s bound; intervals 5 s, 5 s, 30 s and 10 s (audit page 1 only); routes listed | A1.7, A1.8 |
| P3-5 accessibility against the CSS | **FIXED** in the CSS and the mock: `min-height` on chips, badges, inputs; `max(calc(100vh - 14rem), 12rem)` and `scroll-padding-top` on the wrapper; `tabindex="0"`, `role="region"`, `aria-labelledby` on all four wrappers; static tests in A1.10 item 13; the render half stays **UNVERIFIED** | A1.5, A1.10, A1.11, the CSS, the mock |
| P3-6 scope beyond "presentation" and the variable name | **FIXED**: the header lists what A1 decides beyond presentation; the variable is `CIRAK_DASHBOARD_LABEL` with a cross-reference to ADR-026 §2 and ADR-027 (both PROPOSED), raised as Q16 | header, A1.8, A1.12 |
| P3-7 D17 Q8 harness badge | **FIXED**: the badge stays (A1.0 row, A1.4 item 1) and is in the mock (sessions table and detail header) | A1.0, A1.4, the mock |
| P3-8 theme allow-list, query values, history | **FIXED**: the stored theme is checked against `dark`/`light`; query values only through `.value`/`textContent` and allow-listed options; XSS fixture for the query string; the history note | A1.6, A1.10 items 11 and 12, A1.11 |
| P3-9 path shortening ambiguity | **FIXED**: ellipsis plus the last two segments (never inside a segment), a stable 6-hex suffix only when two rows would render identically, full value in `title`, the detail page and the copy control | A1.4 item 1, the mock |

**Out of scope, recorded:** nothing new. Review #476's UNVERIFIED items (the mock is not rendered; the 216-pair
count and D28 are not verifiable from the repository) stay as stated in A1.11; the "216" count is replaced by the
reproducible count above.

### A1.15 Review #480 and #481 dispositions (task #483, 2026-11-21)

Review #480 (Worker B, mandatory): PASS-WITH-NITS, one P2 and nine P3. Delta re-review #481 (Worker C): PASS-WITH-NITS,
two new P3. Every cited line was re-checked on main `cdaf5bd` and on the PR head `839ba4a`: `.btn:active`
(`dashboard-v2.css:237`) and `.btn--primary` (`:238`) as described, `Execution` without a session field
(`internal/dashboard/source.go:22-30`), `AuditSource.Entries()` only (`internal/dashboard/audit.go:78-79`),
`maxListRows = 200` and the truncation note (`internal/dashboard/views.go:20`, `poll.go:96-98`). No finding was false.

**P2-1, before and after (recomputed with the WCAG 2.x formula).** *Before:* a pressed primary button drew `--on-primary`
on `--hairline`: dark `#0d1b3e` on `#333333` = **1.337:1**, light `#ffffff` on `#e0e0e0` = **1.320:1**. *After:* a
`.btn--primary:active` rule after `.btn:active` with the new token `--primary-active` (`#6a9cf5` dark, `#1a4fd6`
light): **6.18:1 dark, 6.70:1 light**, both tested; the pair joins the token set (**124 token pairs + 22 fill pairs**,
0 failures; the minima are unchanged: text 4.62, non-text 4.06); the mutation "primary pressed fill reverted to
`--hairline`" is in A1.10.

| Finding | Disposition | Where |
| --- | --- | --- |
| **#480 P2-1** pressed primary button 1.34:1 / 1.32:1 | **FIXED** (token, rule, tested pair, mutation, counts restated) | A1.1, A1.2, A1.10, the CSS |
| #480 P3-1 + #481 P3-1 `/executions` join set vs the 200-row display cap | **FIXED**: the join set is the scope set **before** the display cap; a footnote on the page; a fixture with more than 200 in-scope sessions | A1.4 item 3, A1.8, A1.10 item 11 |
| #480 P3-2 no session field on the dashboard `Execution` | **FIXED**: S3 adds the harness-qualified session ID to the row; the Session column and link are built from it | A1.8, A1.9 S3 |
| #480 P3-3 A1.0 scope vs the D17 Q4 audit ring | **FIXED**: the scope sentence is limited to the session and execution pages and the audit page is named as the D17 Q4 exception (not ACL-filtered, cited); D17 is conformed to, not reopened | A1.0, A1.4 item 5 |
| #480 P3-4 mock gaps | **FIXED** in the mock: the liveness note says "the Bridge takes no action"; the audit table has a Session column; an archived row with its chip and the filter note; **declined** for the seven `h1`: the mock is a one-page stand-in, stated in A1.11 with the tests that run on real templates instead | the mock, A1.11 |
| #480 P3-5 token-expiry badge and instance label have no slice | **FIXED**: both are S3 (with `Source.Health` and the label variable); S1/S2 omit them | A1.3, A1.9 |
| #480 P3-6 questions without a recommendation | **FIXED**: explicit "Recommended:" on Q6, Q9, Q11, Q12 and Q15 (the questions stay open) | A1.12 |
| #480 P3-7 A1.1 vs A1.4 reserved labels | **FIXED**: the four reserved A2 labels are listed in A1.1 as not rendered until ADR-017 A2 is accepted | A1.1 |
| #480 P3-8 `color-scheme: light` | **FIXED** in both light blocks of the CSS | the CSS, A1.1 |
| #480 P3-9 + #481 P3-2 the JSON twin and the cache key | **FIXED**: the twin uses the same `Source.Executions`; tests on the twin, a cache-key test (`?archived=1` then without within 1 s), and three mutations (unfiltered twin, flagless key, join set cut at 200) | A1.8, A1.10 item 11 and mutations |

**Out of scope, recorded:** nothing new. #481's UNVERIFIED items (the pending-permission row, the archived outline
token and the nested-tint stack are not recomputable from the tokens alone) stay listed in A1.2 as fills; the nested
tint is still not a used pair.

### A1.16 Acceptance record and review #486 dispositions (task #489, 2026-11-21)

**Acceptance.** Marked ACCEPTED per maintainer decision D31 (2026-11-21, issue #117, and the planner comment on PR
#465), after reviews C #476 and #481 and B #480 and #486 with no open P0-P2. A1.12 records Q1-Q16 as decided with the
recommendations, worded as in D31. **One wording difference was found and resolved by D31's own text:** the text of
Q3 named `BRIDGE_DASHBOARD_LABEL` (the name before ADR-026's prefix) while D31 answers Q3 with
`CIRAK_DASHBOARD_LABEL` (and Q16, already in the text, recommended the same); Q3's text now carries D31's name. No
other recommendation differed.

Review #486 (Worker B, delta re-review of `229efc2`): PASS-WITH-NITS, four new P3. Every citation was re-checked on the
head before editing and held.

| Finding | Disposition | Where |
| --- | --- | --- |
| #486 P3-1 pressed-cue ratio vs the 3:1 rule | **FIXED**: the 3:1 non-text pair is the pressed fill against the surface it sits on (dark 6.37 toolbar / 6.10 card, light 5.99 / 6.70), added to the fill list (**26 fill pairs**, 124 token pairs, 0 failures, minima unchanged); the normal-vs-pressed difference (1.26 / 1.37) is stated as a supplementary cue outside the asserted set | A1.2 (rationale 4, fill list), A1.13 |
| #486 P3-2 dark `data-theme` has no `color-scheme: dark` | **FIXED**: `:root[data-theme="dark"] { color-scheme: dark; }` added to the CSS; noted in A1.6 JS-2 | the CSS, A1.6 |
| #486 P3-3 `claudecode:` example not reachable | **FIXED**: `opencode:` today; `claudecode:` only once Claude executions are wired into the dashboard source (the source takes executions from the OpenCode `Manager`, `manager.go:408`) | A1.8 |
| #486 P3-4 mock gaps | **FIXED**: the executions footnote says "includes sessions beyond the first 200 shown on the Sessions page"; the primary control is the Refresh **link** (the JS-3 no-JS fallback) and the toolbar lists it | the mock, A1.4 item 1 |

**Out of scope, recorded:** nothing new.

### A1.17 D34 (2026-11-26): per-page scripts

**Decision (maintainer, D34).** `dashboard.js` had reached 16,273 of its 16 KiB cap (A1.6 and A1.9) with S3b (light
theme) and ADR-017 A2 S5 (badges) still to come. The one script is split; **nothing else in A1 changes** (the JS-1 to
JS-6 behaviours, the A1.7 pause-on-hidden polling, the textContent-only rule, GET only, the CSP).

- **The files.** `dashboard.js` is now the **core** that every page loads: the helpers, JS-6 (menu), JS-1 (copy), JS-5
  (relative times), the A1.7 poll engine and the filter helpers of JS-4, and one namespace, `window.cirakDash`, for the
  page scripts. Three page scripts load after it, in document order, and each stops quietly when the core is missing:
  `dashboard-list.js` (the sessions page: the filter, the table rebuild, the 5 s poll), `dashboard-detail.js` (the
  session page: the field and report updates, the 2 s poll) and `dashboard-audit.js` (the audit page: the row filter).
  The executions, catalog, health and turn pages load the core alone. `theme.js` (S3b) stays a separate file of at most
  1 KiB.
- **Caps (replacing "one script, 16 KiB").** Each script is at most **16 KiB** and all the `dashboard*.js` files together
  at most **32 KiB**; at this change they are 8,186 + 5,785 + 3,416 + 747 = 18,134 bytes. `theme.js` keeps its own cap.
- **What does not change.** The CSP is still `script-src 'self'` with no inline script and no external fetch; every
  script is an empty `<script src="/dashboard*.js"></script>`; the A1.6 forbidden-API scan runs on every script; the
  only request site is the core's poll (GET, `If-None-Match`). The new files are routes of the same table: `GET`/`HEAD`
  only, no data route, no limiter charge, the same security headers as `/dashboard.js`.
- **The pages.** A page loads the scripts its markup needs and no more; the tests read the needs from the HTML hooks
  (`data-poll`, the audit filter and rows), not from the server's own table. A script served and not referenced, or
  referenced and missing, fails the build.
- **Effect on S3b and A2 S5.** A new behaviour gets its own script (or a few bytes in the script of its page); the
  core is the place for what every page needs. No existing slice text is superseded beyond the cap sentence in A1.6.

### A1.18 S3b as built (2026-11-26): light theme, `theme.js` and the toggle

Facts about the code (task #586), no new decision; D34 (A1.17) is unchanged.

- **CSS.** The embedded stylesheet gets the two light blocks of the reference file, comment-free: the
  `prefers-color-scheme: light` block (`:root:not([data-theme="dark"])`) and the toggle blocks
  `:root[data-theme="dark"]` and `:root[data-theme="light"]`; `:root` declares `color-scheme: dark light`. The contrast
  test now runs every A1.2 pair for both themes, over both light blocks, and reproduces the documented light figures
  (minimum text pair 4.65, error chip on the low surface; minimum non-text pair 4.06, the input border on the low
  surface; the dark minimum stays 4.62). The two light blocks must be identical and equal to the reference file, every
  dark colour token must have a light value, and no non-colour token may be redefined.
- **`theme.js`.** 984 bytes (cap 1 KiB, its own cap). The four `dashboard*.js` files are core 8,201 (the frozen namespace added 15
  bytes), list 5,785, detail 3,416 and audit 747, 18,149 together (cap 32 KiB). A blocking `<script src="/theme.js">` in the head of every page and a hidden
  `<button id="theme">` in every top bar; the script is routed with the others (`GET`/`HEAD`, no data route). Storage
  key `cirak-theme`; a stored value other than `dark` or `light` is removed and ignored; the toggle stays hidden without
  script; a blocked or full storage is caught. The only `localStorage` use of the dashboard.
- **Carried P3s.** Cap boundaries over a helper (#584 P3-1), the `unavailablePage` doc comment (#584 P3-2), the scheme
  detector strips tab, CR and LF and trims C0 controls (#585 P3-1), `window.cirakDash` frozen and the hygiene scan raw
  (#585 P3-2), `BRIDGE_DASHBOARD_LABEL` reported as not read by `config check` (#576 P3-1), single-character label cases
  (#576 P3-2), the top bar on 503 pages (#576 P3-4), the whole build revision (#576 P3-5), the cap said in the
  `/executions` footnote (#576 P3-6) and the `?page=` digits-only rule on a full ring (#580 P3-1). The stale manual
  `tests/dashboard/poll_stub.js` is deleted: the node harness tests cover the split scripts.
- **Not done.** The A1.7 poll intervals of the S3a pages (separate task); ADR-017 A2 S5 badges (Worker B).
