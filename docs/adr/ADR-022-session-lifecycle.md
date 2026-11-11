# ADR-022: Session lifecycle (launcher, catalog, close and archive)

Date: 2026-11-11. Status: **PROPOSED**. Design only; no production code,
nothing deployed. It proposes additive contract changes (new tools
`create_session`, `list_catalog`, `archive_session`, the error code
`SESSION_LIMIT`, the closed refusal reasons `non_git` and `not_in_root` on
existing codes, and `session_ref` in the catalog); the seven original tool
shapes do not change. Written for maintainer decision D8 (Phase 7 scope).
Revised 2026-11-11 after review #160 (findings P2-1..P2-3, P3-1..P3-6; see the
fix report on issue #164) and again after reviews #171 and #170 (see the fix
report on issue #174).

Numbering: ADR-022 is reserved for this decision. ADR-021 (Claude Code
adapter) is a separate record; this ADR does not depend on it, and the Claude
Code launcher is out of scope until ADR-021 is accepted.

Must preserve: ADR-010 (single writer, memory-only records), ADR-011 (late
completion), ADR-012 (stateless registry derived from an immutable operator
ACL), ADR-014 (workspace identity and bounded admission), ADR-017, ADR-018 and
ADR-020 (operator-only fenced guard release). It amends none of them **for the
lifecycle tools**. The one place that would amend an accepted ADR is the
existing-tool authorization widening in section 3.2: it is **not** decided here;
it is presented as options and an open question, and if the maintainer chooses
a widening it is a proposed **ADR-012 amendment** (ADR-012 states the registry is
derived exclusively from the immutable operator session ACL and that no dynamic
write grant exists). A deleting admin-socket command (section 7.4) would
likewise be an ADR-020 amendment.

Upstream facts below are static source reading of `anomalyco/opencode` tag
v1.19.4 = commit `148842ef7cb1bc34192b3fff57d0f9721ab9c801` (a fresh sparse
checkout, read only; OpenCode was not run and no Bridge or OpenCode call was
made). Paths are under `packages/opencode/src/` unless prefixed `app/`
(`packages/app/src/`). Anything not settled by source is marked **UNVERIFIED**.
Abbreviations: `grp` = `server/routes/instance/httpapi/groups/session.ts`,
`hnd` = `server/routes/instance/httpapi/handlers/session.ts`, `ses` =
`session/session.ts`.

## 1. Problem

Today a session exists only if the operator created it in OpenCode and listed
its ID in `BRIDGE_ALLOWED_SESSIONS` (ADR-012: immutable ACL, restart to
change). Claude Chat cannot start work in a new workspace, cannot see sessions
across harnesses in one place, and cannot tidy up. D8 asks for three
capabilities: (1) a **launcher**, (2) a **catalog** with aggregate status, (3)
**close and archive**, where Chat decides close/archive without a per-call
approval.

The static per-session ACL cannot cover a session that does not exist yet.
Authorization therefore has to move from "this session ID" to "this directory
tree", and the Bridge has to bound what an autonomous caller can do inside it.

## 2. What OpenCode v1.19.4 supports (verified in source)

| Capability | Native route and payload | Evidence |
|---|---|---|
| Create | `POST /session`, optional body `{parentID?, title?, agent?, model?, metadata?, permission?, workspaceID?}`. **No `directory` field.** | `grp:87` (`create: root`), `grp:203-214`, `ses:260-269`, `hnd:155-176` |
| Directory of a new session | taken from the request's instance context (`?directory=` or `x-opencode-directory`, else server cwd), not from the body | `ses:670-686` (`directory: ctx.directory`), routing as in docs/design/F2_WORKSPACE_ROUTING.md |
| Archive | `PATCH /session/:id` with `{time:{archived:<finite number>}}`; the update handler only acts when `time.archived !== undefined` | `grp:49-57` (`UpdatePayload`), `hnd:183-203` (line 200), `ses:757-759` (`setArchived`), `ses:200` (`ArchivedTimestamp = Schema.Finite`) |
| Unarchive | **No route found.** `ArchivedTimestamp` is `Schema.Finite`, so `null` is not accepted, and an omitted field is skipped by the handler. The upstream web app only ever sends `{archived: Date.now()}` | `ses:200`, `hnd:200`, `app/pages/session/session-archive.ts:52`, `app/pages/layout.tsx:881` and `:1488`; `grep -i unarchive` over `opencode/src`, `core/src`, `app/src`, `sdk` finds nothing |
| Effect of archive on listing | archived sessions are hidden from the default list (`isNull(time_archived)` unless `archived` is requested); the HTTP `ListQuery` has no `archived` parameter | `ses:562`, `grp:30-38` (`ListQuery`) |
| Delete | `DELETE /session/:id`, removes the session **and its children**, publishes `Deleted`; irreversible through the API | `grp:88`, `hnd:178-181`, `ses:606-626` |
| Abort | `POST /session/:id/abort` calls `promptSvc.cancel` | `hnd:232-235` |
| Status | `GET /session/status` (already used by the Bridge for state and guard checks) | `grp:121`, CONTRACTS "Session state and the Bridge guard" |

Three consequences that shape this ADR:

1. **Archive is one-way through the HTTP API.** The maintainer's premise that
   "archive and unarchive are reversible" does not hold at v1.19.4: a client
   can set `time.archived` but cannot clear it. The data is not removed (the
   handler writes one column), so recovery is possible out of band, but not
   through any route the Bridge can call. Whether an archived session can still
   be read and prompted is **UNVERIFIED** (not traced). See decision 4.2.
2. **A new session's directory is decided by request routing, not by the
   body.** The Bridge controls it by sending `?directory=<canonical root-relative
   path>`. This is exactly the F-2 mechanism, so every new directory is an F-2
   bootstrap (section 5.4).
3. **Delete takes the children with it.** One call can destroy a whole tree.

## 3. Authorization: from per-session ACL to root allowlist

### 3.1 Decision

Add an **operator-configured lifecycle root allowlist**, immutable at runtime
like ADR-012's ACL (change = restart). A root is a *trust statement*, not just
a path filter: because opening a directory in OpenCode executes `.opencode`
plugins (F-2), the operator is asserting that everything an OpenCode instance
will load for any directory under that root is trusted.

Configuration (proposed; names are not final):

- `BRIDGE_LIFECYCLE_CONFIG` = path to a JSON file the service user owns, mode
  `0600`, read once at startup. The Bridge never rewrites it.
- Per root: `path` (absolute), `modes` (subset of `create`, `archive`),
  `max_sessions` (cap on non-archived sessions under the root), optional
  `max_depth` for the directory below the root where creation is allowed.
- Global: `create_per_hour`, `archive_per_hour`, `max_total_sessions`
  (section 7).
- Absent or empty: lifecycle tools are not registered (default off, no change
  for existing deployments). With `BRIDGE_READ_ONLY=1` the lifecycle tools stay
  registered and return `FORBIDDEN` for `create_session` and `archive_session`
  before any native mutation, the same pattern CONTRACTS uses for `send_prompt`
  and `interrupt_execution` (CONTRACTS "BRIDGE_READ_ONLY"). Keeping the tools
  listed means a client sees a stable tool set and a typed refusal rather than
  a missing tool. `list_catalog` is read-only and stays available.

Startup validation fails closed: each root must exist, be canonical (no
symlink alias in its own path, same rule as ADR-014), be a directory owned by
the service user or root, not be `/`, and not be contained in or contain
another configured root. Overlaps with `BRIDGE_ALLOWED_ROOTS` are allowed; the
two grants stay distinct (ADR-012 already requires a mutation/workspace grant
to be separate from a metadata grant).

### 3.2 Effective authorization

A session is **authorized for lifecycle actions** when, at call time:

1. its native `directory`, canonicalized and symlink-resolved, lies inside a
   configured root that has the needed mode; and
2. the directory identity matches (ADR-014: device/inode of the accepted
   directory, rechecked immediately before the native call); and
3. its worktree root (the Git top level, or `/` for a non-git directory,
   exactly as in the F-2 note) is **inside the same root**. If the worktree
   root is above the root, the `.opencode` search would walk outside the
   operator's trust statement, so the session is refused with `FORBIDDEN`.
   **Non-git directories are therefore excluded:** a directory with no Git top
   level has worktree root `/`, which is never inside a root, so
   `create_session` and `archive_session` refuse it (reason `non_git`; CONTRACTS
   "Discovery bootstraps native instances" records that the `.opencode` walk of
   a non-git directory goes up to `/`). A configured root that is itself not a
   Git worktree is still valid as a container: sessions are created in
   directories below it whose Git top level lies inside the root. An operator
   who wants to launch in a plain directory must `git init` it first (the
   Bridge never creates or initializes directories).

**How the Git top level (worktree root) is learned.** The Bridge uses **no
`git` subprocess** (the repository has no `os/exec` in non-test code, and
CONTRACTS "Confined metadata reads replace Git subprocesses" records why:
repository-controlled configuration must not run as the Bridge user). It reuses
the confined metadata reads of ADR-012/H4-A (`internal/repository`, `Inspect`,
reads through `policy.OpenStorage`/`os.Root`), extended for lifecycle use by a
check that every step stays inside the lifecycle root. The procedure:

1. Start at the canonical session directory and ascend one level at a time (at
   most 128, as `inspect` does), never past the lifecycle root. At each level
   `Lstat` `<dir>/.git`.
2. `.git` absent (genuinely absent, `os.IsNotExist`): go up. Reaching the
   lifecycle root without finding one is `non_git` (refused).
3. `.git` is a **directory**: this directory is the top level; the git
   directory is `.git` itself.
4. `.git` is a **file** (linked worktree or submodule): it must be a single
   line `gitdir: <path>` (no CR/LF, at most 4096 bytes). The top level is the
   directory that contains the `.git` file. The pointer is resolved
   (`pointer()` in `internal/repository/repository.go`) and the resulting git
   directory, and its `commondir` target if present, **must lie inside the
   lifecycle root**. A pointer that leaves the root, or a pointer or
   `commondir` that cannot be opened through the confined storage, refuses with
   `FORBIDDEN` (reason `not_in_root`). The Bridge does not follow it outside.
5. `.git` is a symlink, unreadable, not a directory and not a valid `gitdir:`
   file, or the layout is one the bounded reader does not support (reftable,
   bare, malformed): refuse (reason `non_git`; fail closed, never ascend past
   it).
6. The nearest `.git` wins: a repository nested in another repository uses the
   inner top level.

**Residual that the confined reader cannot see: `core.worktree`.** Git's
`core.worktree` setting (and `include`/`includeIf` sections of the git
config) can relocate the work tree. The H4-A reader deliberately **never reads
`config`** (ADR-012 correction), so it cannot detect this, and OpenCode, which
asks Git itself, may then pick a different worktree root than the Bridge does.
That would break rule 3. Two ways to close it, both proposals, neither
decided (open question 9): (i) a lifecycle-only, bounded, include-free scan of
`<gitdir>/config` that refuses on any `worktree` key under `[core]` or any
`include`/`includeIf` section (an explicit, narrow exception to the "no config
consulted" rule, for lifecycle checks only); or (ii) accept the residual and
make it an operator duty that no allowlisted root contains such a repository.
Until the maintainer chooses, the ADR does not claim rule 3 is complete for
repositories that use `core.worktree`.

**The eleven existing tools keep their current check, unchanged by this
section.** That check is a conjunction, not a union
(`internal/security/policy.go:56-66`, `Allows`): the session ID must be in
`BRIDGE_ALLOWED_SESSIONS` (`p.Sessions[id]`) **and** its directory must lie
under a root in `BRIDGE_ALLOWED_ROOTS` (`directoryAllowed`). The server
registers eleven tools (the seven original tools plus the four H4-A discovery
tools, `internal/mcp/server.go:107,241`; CONTRACTS still says "seven tools" in
`:168`). Which ACL path each uses, and what the options below would change:

| Tool | ACL path today | O1 (narrow) | O2 (wide) |
|---|---|---|---|
| `send_prompt`, `interrupt_execution` (mutating) | session-scoped `authorized` (`server.go:57-68`, `AllowsContext`); `BRIDGE_READ_ONLY` returns `FORBIDDEN` | would act on a Bridge-created session | would act on any session under a lifecycle root, human-created included |
| `get_execution_status`, `get_execution_result` | by execution ID, bound to the execution's pinned native session (which call re-checks the ACL is not traced here: UNVERIFIED) | follows the session grant | follows the session grant |
| `get_session`, `read_session_history`, `get_harness_capabilities` | session-scoped `authorized` (`server.go:277`) | would read a Bridge-created session | would read any session under a root |
| `list_sessions`, `find_sessions`, `list_repositories`, `get_repository` | discovery pipeline, `AllowsContext` per candidate session (`discovery.go:306`), derived from the immutable ACL (ADR-012) | **would list a created session** (a read-only exposure widening) | **would list every session under a root** (largest read-only widening) |

The four discovery tools matter: under O1 or O2 they would start showing
sessions that are not in `BRIDGE_ALLOWED_SESSIONS`, which changes ADR-012's
statement that discovery is derived exclusively from the immutable operator
ACL. That is part of the proposed ADR-012 amendment, not a side effect. Under
O3 nothing changes. Everything in sections 3.1 to 3.3 and 4 to 7 above concerns
only the lifecycle tools (`create_session`, `archive_session`,
`list_catalog`); the lifecycle grant does not by itself let any of the eleven
existing tools act on a session.

A created session has to be usable by the existing tools for the launcher to be
useful, and that needs a decision this ADR does not take. The options:

- **(O1) Narrow grant: sessions the Bridge itself created in this process.**
  The existing-tool check would accept a session that is **not** in
  `BRIDGE_ALLOWED_SESSIONS` only if (i) this writer process created it
  (the in-memory creation event is the sole authority, recorded when the
  native `POST /session` returned and the answer was verified; never read back
  from a journal, a file or native metadata), (ii) its directory is still under
  a lifecycle root and the ADR-014 identity still matches. The lifecycle caps
  are checked **at creation only**: a later excess (for example the root passing
  `max_sessions` through sessions created natively) refuses new creates but does
  not revoke an existing grant; this is a deliberate choice so a grant does not
  depend on unrelated activity. After a restart those sessions lose the
  grant (fail-closed) until the operator adds the IDs to
  `BRIDGE_ALLOWED_SESSIONS`. This is a dynamic, in-process grant, so it is a
  proposed **ADR-012 amendment** (narrow).
- **(O2) Wide grant: every session whose directory is under a lifecycle root.**
  Stateless and restart-proof, but it also authorizes the existing tools for
  pre-existing and human-created sessions under the root, which goes beyond D8
  (D8 covers sessions the Bridge creates). A proposed **ADR-012 amendment**
  (wide).
- **(O3) No widening.** The existing-tool check stays exactly as today
  (ADR-012 intact). A created session is usable by the existing tools only after
  the operator appends its ID to `BRIDGE_ALLOWED_SESSIONS` and restarts. The
  launcher then creates sessions the operator must enable out of band, which
  limits its value. The optional first prompt in `create_session` is not
  available in this option.

The maintainer decides (open question 7). Slices S2 and S4 below name the
parts that depend on the choice. In every option the lifecycle grant is
computed per call from live native data and current config, and nothing is
cached as a grant.

### 3.3 Registry (location, modes, restart behavior)

The Bridge keeps a **memory-only registry** of sessions it created, with
`{session_id, root, created_at, idempotency key hash}` and counters for the
rate limits. It lives in the single writer process (ADR-010), is guarded by the
same host-ownership rule, and **is not an authority for the lifecycle tools**
(it would be the grant record only under O1, section 3.2):

- authority for the lifecycle tools comes only from section 3.2 (config + live
  native directory), so a restart loses no lifecycle authorization and the
  registry cannot be forged by editing a file or by a journal replay (the
  project invariant: no grant restoration from journal history alone). What the
  existing tools may do with a created session depends on the O1/O2/O3 decision in
  section 3.2; under O1 a restart does drop that grant, by design;
- the registry is used for idempotent replay of `create_session` and for
  counting created-per-hour; per-root `max_sessions` is counted from **native
  data** (a bounded catalog read), not from the registry, so it stays correct
  after a restart and for sessions created outside the Bridge. **An incomplete
  count refuses the create and never counts as zero:** if the read is truncated
  (more sessions than the ID-sorted window of 200 / offset 1000), times out, or
  any page fails, the count is only a lower bound, so `create_session` returns
  `SESSION_LIMIT` (cap state unknown) or `UPSTREAM_TIMEOUT` and makes no native
  write;
- the rate counters (`create_per_hour`, `archive_per_hour`) also live in this
  registry, so **a restart resets them**; each window is still bounded by the
  caps, but an operator who restarts the Bridge repeatedly gets a fresh budget
  each time (a limit of the memory-only design, stated here);
- after a restart the registry is empty. A retry of `create_session` with the
  same key after a restart is a new request (documented duplicate risk, section
  4.3); the caps bound the damage, and the catalog shows any duplicate.

Provenance marker: the Bridge sets `metadata` on creation to
`{"hb_origin":"bridge","hb_key":"<first 16 hex of sha256(key)>"}` (the create
body accepts `metadata`, `ses:260-269`). It is **a label and a lookup hint
only**: any client with direct OpenCode access can write or strip metadata, so
it is never used for authorization or counting.

### 3.4 Coexistence with static sessions

Static sessions keep working unchanged. A static session outside every
lifecycle root gets **no** lifecycle rights (it cannot be archived by Chat); a
static session inside a root with `archive` mode can. Operators who want to
keep a session safe from `archive_session` keep it outside the roots or in a
root configured with `create` only.

## 4. Launcher: `create_session`

New tool, additive; the eleven existing tools are unchanged.

### 4.1 Contract

Input (all strings; unknown fields rejected, like the other tools):

| Field | Rule |
|---|---|
| `root` | must equal one configured root path (or the root's `id`) |
| `subdirectory` (optional) | relative, clean, no `..`, no symlink components, must already exist, at most `max_depth` below the root |
| `title` | optional, 1–200 characters, no control characters |
| `prompt` (optional first prompt) | same bounds as `send_prompt` (1–32768 bytes) |
| `idempotency_key` | 8–128 bytes, as in `send_prompt` |

Output: `{session_id, root, directory, title, created: true, execution?}`; when a
first prompt is given, `execution` is the normal `Execution` from the existing
`send_prompt` path (no second admission mechanism).

Flow (each step bounded by the 5 s native bound; no step retried automatically):

1. Authenticate, `BRIDGE_READ_ONLY` check, rate-limit and cap check
   (`create_per_hour`, `max_sessions` from a native catalog read,
   `max_total_sessions`). Refuse with `SESSION_LIMIT` (new code, retryable
   after the window) when exceeded, **and also when the native count is
   incomplete** (truncated or timed out, section 3.3): an unknown count is
   never treated as under the cap.
2. Resolve `root[/subdirectory]` to a **fully resolved** canonical path (no
   symlink components); capture directory FileInfo (ADR-014); compute the Git
   top level by the procedure of 3.2 and verify worktree-root containment (3.2
   rule 3). The Bridge **never creates directories**: a missing path is
   `NOT_FOUND`. Cloning, worktree creation and `mkdir` are out of scope. The
   fully resolved path (not the operator-spelled one) is what step 4 sends.
3. Same key and same input: return the recorded result (memory-only). Same key,
   different input: `IDEMPOTENCY_CONFLICT`.
4. `POST /session?directory=<canonical path>` with body
   `{title, metadata: {hb_origin, hb_key}}` (`grp:203-214`). No `permission`,
   `agent`, `model`, `parentID` or `workspaceID` is accepted from the caller:
   the Bridge never lets Chat pre-set a permission ruleset (`ses:260-269`
   accepts one) because that would sit next to the "no implicit native
   permission approval" invariant.
5. Verify the answer: the returned `directory` must canonicalize to the
   requested path and the identity must still match. On a mismatch the Bridge
   reports `UNKNOWN_OUTCOME` (never `completed`), does not use the session, and
   writes the audit field `bootstrap_may_be_outside_root=true`, because the
   POST in step 4 may already have bootstrapped an instance (and loaded
   `.opencode` plugins) at a path other than the one the Bridge checked. The
   caller finds the stray session through the catalog and the operator treats
   the audit line as a possible F-2 execution event.

   **Residual (check then use).** OpenCode resolves the path string itself
   *after* the Bridge's ADR-014 recheck (`ses:676-688`, `directory:
   ctx.directory` from request routing), and the bootstrap and plugin import
   happen inside the POST (section 5.4). A directory or symlink swapped between
   the Bridge's check and OpenCode's use is therefore not prevented by the
   Bridge, and step 5 detects it only after the bootstrap ran. The actor who can
   do this is anyone who can write inside the root, including the agents the
   Bridge drives. **Operator duties** that close it: the lifecycle root and every
   directory component below it in which a session may be created must not be
   writable or replaceable by the agent users (or any user other than the
   operator); no agent-writable symlinks may exist inside the root; the root
   lives on a filesystem the operator controls. The Bridge cannot enforce
   these, so the trust statement of 3.1 includes them.
6. Optional first prompt (only if the maintainer chooses O1 or O2 in section
   3.2; under O3 the field is rejected): through the existing `send_prompt`
   admission with a derived key, so the writer, guard and replay rules of
   ADR-010/011 apply unchanged. The derived key is a **hash**, never a
   concatenation: the first 32 hex characters of `sha256(key || ":first")`
   (32 bytes, inside the 8-128 byte `send_prompt` bound), because a 123-128
   byte create key plus a suffix would exceed 128 and fail after the session
   already exists (`manager.go:187`).
7. Audit and return.

### 4.2 Failure modes

| Case | Result |
|---|---|
| Root not configured, subdirectory escapes, path missing, worktree root above the root | `FORBIDDEN` / `INVALID_INPUT` / `NOT_FOUND`; no native call |
| Non-git directory or an unsupported or unreadable `.git` layout | `FORBIDDEN`, closed reason `non_git`; no native call |
| `gitdir:`/`commondir` target outside the lifecycle root | `FORBIDDEN`, closed reason `not_in_root`; no native call |
| Native count of existing sessions incomplete (truncated, timed out) | `SESSION_LIMIT` (cap state unknown) or `UPSTREAM_TIMEOUT`; no native write |
| Returned `directory` differs from the requested path (step 5) | `UNKNOWN_OUTCOME`; audit `bootstrap_may_be_outside_root=true`; session not used |
| Cap or rate exceeded | `SESSION_LIMIT`, retryable, no native call |
| Native timeout or lost response on `POST /session` | `UNKNOWN_OUTCOME`, retryable false. **Never auto-retried or replayed** (the project's rule for uncertain native mutations). The caller finds a stray session through the catalog (the `hb_key` hint) |
| Native 4xx/5xx | `UPSTREAM_FAILURE` |
| Create succeeded, first prompt refused | session is returned with `execution` absent and the prompt error; the session is not deleted |
| Restart between create and a retry | new request (3.3) |

### 4.3 Audit

AUDIT-01 gets `event=session_create` (one line per call, every return path),
fields: `tool=create_session`, `outcome`, `accepted`, `error_code`, `root_id`
(index or hash, not the free path), `session_id` when well formed,
`key_hash`, `limit_state` (counts after the call). No title and no prompt text
(same rule as the existing lines).

### 4.4 Claude Code

After ADR-021, `create_session` takes a `harness` selector. Until then only
`opencode` is accepted. The Claude Code launcher needs its own native-API
evidence and is not designed here.

## 5. Bridge-side interactions

### 5.1 Guard, single writer, idempotency (ADR-010/020)

- `create_session` is a mutation: it runs only in the process that owns the
  writer role, and `BRIDGE_READ_ONLY` makes it return `FORBIDDEN` (section 3.1).
  Independent processes do not share the registry.
- `archive_session` is excluded from `send_prompt` and the other writers **by
  the manager's per-session reservation, not by an execution token.** An
  archivable session normally has no run at all (precondition 7.3.2 requires
  that no execution is running, unknown or guard-held), so there is no
  `x.op` token to take (`x.op` is a per-run channel,
  `internal/execution/manager.go`). The mechanism is the reservation
  `busy[session]` (ADR-010) that `Send` already takes before any network IO
  (`manager.go:232-238`) together with the reconcile slot: `archive_session`
  takes `busy[session]` for the duration of its re-check and `PATCH` (a
  concurrent `send_prompt` then gets `SESSION_BUSY`, an interrupt or guard
  release has no run to act on), and releases it afterwards. It never uses
  `x.op`.

  **Representation (implementation requirement).** The `busy[session]` value is
  an execution ID today, and existing readers rely on that. The manager gains a
  distinct **lifecycle reservation kind**: a separate marker that `Send` and
  every other admission path check together with `busy`, and that
  `SessionGuard` reports as held. While it is held (the seconds between the
  re-check and the `PATCH`), `get_session` and discovery show the existing
  conservative value `submitting` plus an additive `guard_reason: "archive"`
  (no new `state` enum value, so clients keep working), and a concurrent
  `send_prompt` gets `SESSION_BUSY`. The reservation has **no run**: the ADR-020
  release (`releaseHolder` compares execution IDs) and the ADR-011 promotion /
  reconcile paths never act on it, and the admin release refuses a session whose
  guard is a lifecycle reservation. It is released on every exit path (return,
  panic, timeout) and is never persisted. Tested in S6.

  **Residual race (stated limitation, not a decision):** the reservation excludes
  Bridge-mediated writers only. A native client or a human can start a turn in
  OpenCode between the Bridge's re-check and its `PATCH`; the Bridge cannot
  exclude that (ADR-010 operator exclusivity is a deployment rule, not
  something the Bridge enforces). The consequence is that a session which just
  became busy is archived (hidden from the default list, one-way through the
  API); the data is not lost.
- A guard held by an execution (including `unknown` with the guard retained)
  blocks archive, as does any running state.

### 5.2 Catalog and bounded reads (F-3)

Reuses ADR-012's bounded reads (`internal/bounded`, per-read 5 s bound, total
discovery 10 s). See section 6.

### 5.3 Directory identity (ADR-014)

The directory identity captured at create time is the baseline for the later
prompt on that session; the existing recheck points apply unchanged.

### 5.4 F-2: new roots bootstrap instances and execute plugins

`POST /session?directory=<path>` boots the instance for that directory once
(`.opencode` walk, `.gitignore` write, background npm install with scripts
disabled, plugin import: see CONTRACTS "Discovery bootstraps native instances"
and the F-2 note in docs/ROADMAP.md). Consequences, all stated as operator
duties and as Bridge limits:

- listing a root in `BRIDGE_LIFECYCLE_CONFIG` means the operator trusts the
  `.opencode` content of every directory a session may be created in, up to the
  worktree root, which is why rule 3 of 3.2 requires the worktree root inside
  the root;
- `create_session` is therefore **not side-effect free** on the native side
  even though the Bridge executes nothing;
- the Bridge cannot detect a malicious `.opencode` directory; it only bounds
  where it will trigger a bootstrap;
- a session created in a subdirectory whose `.opencode` was added after the
  operator's review is outside what this ADR can protect (**residual risk**).

## 6. Catalog and aggregate status

### 6.1 Tool: `list_catalog` (read-only)

New read-only tool; `list_sessions` and `find_sessions` keep their shapes.

Filters (all optional, exact match): `root`, `state`
(`idle|running|waiting_for_permission|unknown`, the contract spelling in
CONTRACTS "Session state and the Bridge guard", mapped from the existing
ADR-012 state semantics; **`archived` is not a v1 filter value**, see section 6.3),
`updated_since`,
`created_by_bridge` (label only), `harness_name` (the harness kind, for
example `opencode`; not the same field as `session_ref.harness`, which is the
instance-qualified `harness:<hash>` identity of 6.2). Bounds as `find_sessions`: limit
default 50, max 200; offset 0–1000; ID-sorted; fresh view, not a snapshot; one
total deadline of 10 s; errors never return silent partial results. Ordering by
recency is a filter on `updated_since`, not a sort, to keep ID-sorted paging.

### 6.2 Aggregate across harnesses

ADR-012 states that multi-harness aggregation needs "a future versioned
instance-qualified session ID" and that the current IDs are not unique across
servers. This ADR takes **a first step toward that requirement, not the
requirement itself**: catalog entries carry
`session_ref = {harness: "harness:<hash>", session_id: "<existing id>"}`, a
composite versioned inside the new `list_catalog` contract. It is not the
ADR-012 versioned instance-qualified session ID, and it does not reinterpret the
seven-tool contract; a later ADR-012 follow-up would define the real ID. The
existing `opencode:<native-id>` form is unchanged for the other tools and is
accepted as before when only one harness is configured. Aggregate counts
(`{total, by_state, by_root}`) are computed from the same bounded page set and
labelled with the page window; they are not whole-fleet totals. Claude Code
entries appear after ADR-021 and are omitted (not guessed) before.

### 6.3 Archive state

There is no `archived` filter or flag in v1. An `archived` state could be
added only if the native listing can show it, gated on the UNVERIFIED read
path below. The HTTP list has no `archived` parameter at v1.19.4 (`grp:30-38`), so
**archived sessions are not visible to the Bridge catalog** unless `get` by ID
works on them (**UNVERIFIED**). The Bridge therefore also records, in memory
only, the IDs it archived since startup, for audit and for the answer to "did I
archive this". That memory is lost at restart by design.

## 7. Close and archive

### 7.1 Tools

- `archive_session {session_id}`: sets `time.archived` via `PATCH`
  (`hnd:183-203`). One session per call; no bulk form, no pattern, no
  "archive all". **The `PATCH` body is fixed by the Bridge to exactly
  `{"time":{"archived":<finite number>}}` and nothing else, never built from
  caller input.** Upstream `UpdatePayload` also accepts `title`, `metadata` and
  `permission` (`grp:49-57`), and the handler merges a permission ruleset into
  the session's (`hnd:194-199`); a pass-through body would open a native
  permission-mutation path next to the "no implicit native permission approval"
  invariant. The timestamp is the Bridge's own clock value. Tested in S6.
- `close_session`: **not proposed as a separate action in this draft, and
  that is a design choice, not a consequence of upstream** (D8 lists close).
  OpenCode has no "close" other than abort (stop a running turn), archive and
  delete; abort is already `interrupt_execution`, and archive is the closest
  to "put away". Whether D8's "close" should be a separate tool (for example
  one that interrupts a running execution and then archives) is **open
  question 8**; the maintainer decides.
- `unarchive_session`: **not offered**, because the native API has none
  (section 2). A fake one would need Bridge-side hiding (see 7.2, option B).

### 7.2 Archive: decision input for the maintainer

The justification for letting Chat archive without a per-call approval was
"reversible". At v1.19.4 it is not reversible through the API, so the
maintainer must choose the semantic:

- **(A) Native archive via `PATCH`.** Durable, no new Bridge state, matches what
  the OpenCode UI does. One-way for every API client; undo needs an operator
  action outside the Bridge (**UNVERIFIED**: not traced, would touch OpenCode
  storage and is out of scope for the Bridge). The session data is not removed.
- **(B) Bridge overlay "archived".** Reversible by the Bridge: the Bridge hides
  the session from its own catalog and refuses prompts to it, and the native
  state is untouched. Cost: the overlay is state the Bridge must keep; if it is
  memory-only (ADR-010), a restart silently un-archives everything, and if it is
  persisted that is new persistent state with its own recovery and
  no-restoration-from-journal questions.

**Recommendation: (A), which requires the maintainer decision in open
question 1.** It is not accepted. D8 recorded only that Chat may decide archive
because archive is reversible; it did not accept a one-way archive. (A) is
recommended because it adds no Bridge state and preserves the data, and it
would run with the caps in section 8. If the maintainer wants real
reversibility, choose (B) and open a follow-up ADR for the overlay's
persistence. **The maintainer decides.**

### 7.3 Preconditions (both options), fail-closed

`archive_session` refuses, with no native write, unless all of these hold at
call time and again immediately before the `PATCH` (re-check while holding the
`busy[session]` reservation of section 5.1, then write; there is no run token
for an idle session):

1. the session passes section 3.2 with `archive` mode;
2. native status is `idle` or absent (OpenCode deletes idle entries from
   `/session/status`, `session/status.ts:42-44`, and ADR-017/018 treat absence
   as idle) and **no** Bridge execution for it is `running`,
   `unknown` or has a held guard (`SESSION_BUSY`);
3. no permission request is pending (ADR-018 view);
4. the session has no live children that are running (the native tree);
5. the session is not the one currently driving this MCP call (a session cannot
   archive itself away mid-turn; **UNVERIFIED** how Chat identifies itself, so
   the Bridge implements this only for sessions it knows are in an active
   execution, which rule 2 already covers).

Native errors map as: not found → `NOT_FOUND`; timeout or lost response →
`UNKNOWN_OUTCOME` (not retried; the catalog or `get_session` tells the result);
anything else → `UPSTREAM_FAILURE`.

Audit: `event=session_archive`, `tool=archive_session`, `outcome`, `accepted`,
`error_code`, `session_id`, `root_id`, `archived_in_window` count. One line per
call on every return path.

### 7.4 Permanent delete: options and recommendation

Permanent delete is irreversible and removes the whole child tree (`ses:606-626`).
The project invariant requires explicit approval for destructive operations.
Options:

- **(a) Not exposed to Chat.** `delete` does not exist in the Bridge. An
  operator who needs it uses OpenCode directly. A later operator-only delete
  command on the ADR-020 admin socket would be an **ADR-020 amendment**, not an
  extension to assume: ADR-020 and `internal/admin/admin.go` define
  `release_guard` as the only operation, so adding delete needs its own review.
- **(b) Exposed with an explicit confirm token.** A two-step flow:
  `request_delete` returns a single-use, short-lived token bound to
  `{session_id, tree size}`; `confirm_delete {token}` performs it. This keeps
  Chat in the loop but a prompt-injected Chat can complete both steps, so the
  token only guards against accidents, not against injection, unless the token
  is delivered over a channel Chat cannot read (not available today).
- **(c) Maintainer waiver** of the invariant for delete. Not recommended: it
  removes the only control on an irreversible action under adversarial input.

**Recommendation: (a).** Chat gets create, catalog and archive; delete stays an
operator action. The maintainer decides; this ADR does not waive the invariant.

## 8. Threat model

| Threat | Control |
|---|---|
| Prompt injection drives mass archive | one session per call; `archive_per_hour` cap (default 10); precondition 7.3; archive only under configured roots with `archive` mode; audit line per call; `BRIDGE_READ_ONLY` makes the tool return `FORBIDDEN`; cap reached returns `SESSION_LIMIT` and stops |
| Prompt injection drives mass create (runaway launcher) | `create_per_hour` (default 10), `max_sessions` per root (default 20), `max_total_sessions` (default 50), no bulk, no recursion (a created session's own tool calls count against the same budget because the caps are Bridge-wide) |
| Disk or session exhaustion | the caps above, counted from native data; no directory creation; first-prompt size bounds as `send_prompt` |
| Escape from the root (`..`, symlink, replaced directory, worktree above the root) | clean-path rule, symlink resolution, ADR-014 identity recheck, worktree-root containment (3.2). **Residual:** a swap between the Bridge's recheck and OpenCode's use is not prevented and is detected only after the bootstrap (4.1 step 5, `UNKNOWN_OUTCOME` + `bootstrap_may_be_outside_root`); closed only by the operator duties in 4.1 (root and its components not writable by agent users) |
| Plugin execution in a new directory (F-2) | operator trust statement per root, no mkdir by the Bridge, residual risk documented (5.4) |
| Forged provenance metadata | metadata is a label only; counts and authority come from native directory and config |
| Replay of an uncertain create after restart | `UNKNOWN_OUTCOME`, no auto-retry; documented duplicate risk; caps bound it |
| Archive racing a Bridge prompt or guard release | `busy[session]` reservation and re-check before write (5.1, 7.3); a native or human writer can still race the `PATCH` (residual, stated in 5.1) |
| Log flooding by refused calls | one line per call; refusals before parsing heavy input; limits apply to tool calls at the ingress (existing token and body bounds) |
| Same-UID attacker | out of scope, as in the ADR-020 trust boundary |

Defaults are proposals for the maintainer; the numbers are inputs to a decision,
not measured values.

## 9. Options considered for the authorization model

1. **Dynamic append to `BRIDGE_ALLOWED_SESSIONS`** (a mutable ACL file).
   Rejected: breaks ADR-012's immutable ACL, and a mutable grant file is the
   "grant restoration from history" shape the invariants forbid.
2. **Per-session grant stored in a Bridge database.** Rejected: new persistent
   authority, conflicts with ADR-010 memory-only records.
3. **Root allowlist with stateless derivation (proposed for the lifecycle
   tools).** Authority is config plus live native directory, like ADR-012's
   read-through registry. Whether it extends to the existing tools is the open
   O1/O2/O3 decision in section 3.2, not a choice made by this option.

## 10. Implementation plan (PR slices)

Each slice is one task, one branch, one PR, with RED before GREEN where
practical. Mutations are the ones a reviewer should re-run.

| Slice | Content | Tests | Mutations |
|---|---|---|---|
| S1 | Lifecycle config parser and root validation (no tools) | table tests for every startup refusal in 3.1 (non-canonical, overlap, `/`, missing, wrong owner, mode) | accept an overlapping root; skip the canonical check |
| S2 | Lifecycle authorization (3.2 rules 1-3), including the Git top level procedure (no subprocess, confined reads) and the non-git exclusion. The existing-tool part (O1/O2/O3) is **not** in S2: it waits for the maintainer decision (open question 7) and, if O1 or O2 is chosen, for an ADR-012 amendment | RED test for each of: plain `.git` directory; `.git` file (linked worktree) inside the root; `.git` file whose `gitdir:` leaves the root (refused `not_in_root`); `commondir` leaving the root; `.git` symlink (refused); malformed or multi-line `gitdir:`; unsupported layout (reftable/bare); nested repository (inner wins); ascent reaching the root with no `.git` (`non_git`); depth limit; if open question 9 option (i) is chosen, `core.worktree` and `include` present (refused). Also symlink, replaced-directory, worktree-above-root, root with `create` only, static session outside roots; the existing-tool check shown unchanged (`policy.go` conjunction) | drop rule 3; compare paths without resolving symlinks; let a non-git directory pass; follow a `gitdir:` pointer outside the root; ascend past a symlinked `.git`; run a `git` subprocess |
| S3 | `list_catalog` read-only, instance-qualified `session_ref`, bounds | limit/offset/deadline, filters, no silent partial result, archived not visible | remove the deadline; drop a filter |
| S4 | `create_session` against a fake native with `POST /session?directory=`. The optional first prompt is included only if O1 or O2 is chosen; under O3 it is absent | every row in 4.2, idempotent replay, conflict, caps, `UNKNOWN_OUTCOME` never retried, no `permission`/`agent` accepted, `FORBIDDEN` under `BRIDGE_READ_ONLY`; a 128-byte create key still yields a valid first-prompt key (hash-derived, 32 bytes); an incomplete or timed-out native count refuses the create with no native write; a returned directory that differs from the request is `UNKNOWN_OUTCOME` with `bootstrap_may_be_outside_root`; the fully resolved path is what is sent | retry on timeout; accept a caller `permission`; cap disabled; derive the first-prompt key by concatenation; treat an incomplete count as zero; ignore the directory mismatch |
| S5 | Audit lines for create and archive | one line per call on every return path, no free text | skip an audit on a refusal path |
| S6 | `archive_session` under a lifecycle reservation kind in the manager (never `x.op`, never an execution ID) | preconditions 7.3 each refuse with no native write; a concurrent `send_prompt` gets `SESSION_BUSY` while the PATCH is in flight; re-check before write; rate cap; `UNKNOWN_OUTCOME`; `FORBIDDEN` under `BRIDGE_READ_ONLY`; **the PATCH body is exactly `{"time":{"archived":<n>}}` whatever the caller sends (a spy native records the body)**; while the reservation is held `get_session` shows `submitting` with `guard_reason: archive`, `releaseHolder`/the admin release/reconcile never act on it, and it is released on every exit path including panic | drop the pre-write re-check; archive while a guard is held; skip the reservation; **forward a caller-supplied `title`/`metadata`/`permission` in the body**; store the reservation as a fake execution ID that ADR-020 release could match |
| S7 | CONTRACTS and runbook; ADR status update after acceptance | doc review | n/a |
| S8 (only if the maintainer picks 7.2 B) | overlay persistence design, its own ADR | | |

Delete (7.4 b or c) is a separate slice and ADR amendment, only on a
maintainer decision.

## 11. Live acceptance outline (R7)

Run against the real OpenCode v1.19.4 and the real Bridge, on a disposable root,
with the maintainer present for steps marked M. Evidence is captured the way
H4 did (native DB, filesystem, journal, independent rebuild).

1. Operator config with one root (create+archive) and caps set low (for example
   3 sessions, 3 creates/hour). Startup refusals demonstrated (bad root).
2. `create_session` for a new subdirectory; compare the native `directory`
   with the request; confirm the instance bootstrap (F-2) happened only in that
   root; prompt the new session through `send_prompt` without editing the static
   ACL.
3. Exceed each cap; confirm `SESSION_LIMIT` with no native call (native request
   log).
4. Lost-response simulation on create (fault injection on the native side, or a
   killed Bridge between POST and response): `UNKNOWN_OUTCOME`, no replay,
   catalog finds the stray session by the `hb_key` hint.
5. Archive an idle session; confirm the native `time_archived`; confirm a
   second archive and an archive on a busy/held-guard session refuse with no
   native write. Try to find any route to unarchive (expected: none); record the
   result for decision 7.2. M
6. Prompt-injection drill: a prepared prompt that asks Chat to archive and
   create repeatedly; confirm the hourly caps stop it. M
7. Restart the Bridge: lifecycle authorization of created sessions persists
   (derived); what the existing tools may do with them follows the O1/O2/O3
   decision (under O1 the grant is gone until the operator lists the ID);
   registry empty and rate counters reset, replay of an old key is a new
   request (documented).
8. Skips and anything not run are reported as UNVERIFIED, not PASS.

## 12. Open questions for the maintainer

1. **Archive semantics (7.2):** accept (A), a native archive that is one-way
   through the API, or require (B), a reversible Bridge overlay? This overturns
   the "reversible" premise in D8.
2. **Delete (7.4):** confirm option (a), not exposed to Chat.
3. **Default caps (section 8):** are 10 creates/hour, 10 archives/hour, 20
   sessions per root and 50 total acceptable starting points?
4. **Provenance label:** is a best-effort `metadata` label acceptable, given it
   is never authoritative?
5. **Archived visibility:** is it acceptable that archived sessions drop out of
   the Bridge catalog (6.3), or is a read path for them required?
6. **Scope of archive:** may Chat archive sessions it did not create but that
   live under an `archive` root, or only sessions the Bridge created in this
   process (a stricter, restart-fragile rule)?
7. **Existing-tool authorization for created sessions (section 3.2):** O1 (narrow,
   in-process grant for sessions this writer created; proposed ADR-012
   amendment), O2 (wide, every session under a lifecycle root; proposed
   ADR-012 amendment, beyond D8), or O3 (no widening; the operator lists the ID
   and restarts)? The ADR does not choose.

8. **`close_session` (section 7.1):** keep "close" as abort (`interrupt_execution`)
   plus archive with no separate tool, or add a `close_session` that
   interrupts a running execution and then archives? D8 lists close; this draft
   proposes no separate tool, which is a design choice, not an upstream limit.
9. **`core.worktree` and git `include` in the top-level computation
   (section 3.2):** (i) a lifecycle-only, bounded, include-free scan of the git
   `config` that refuses such repositories (a narrow exception to ADR-012's
   "no config consulted"), or (ii) accept the residual and require the operator
   to keep such repositories out of every lifecycle root? The ADR does not
   choose.

## 13. Limitations of this record

- Static reading of one upstream commit; behavior of an archived session on
  `get` and on prompt, and any out-of-band unarchive, are UNVERIFIED.
- No code, test or live run was done for this ADR.
- The number defaults, tool names and config keys are proposals.
- How Chat identifies "the current session" to the Bridge is UNVERIFIED, so
  precondition 7.3.5 is only enforced where the Bridge knows an active
  execution.
- The Git top-level procedure (3.2) reuses the logic of the existing
  `internal/repository` reader by reading its code; it was not run, and the
  lifecycle extension (never ascend past the lifecycle root, containment of
  `gitdir:`/`commondir`) is a design, not tested behavior.
- Which call re-checks the ACL for `get_execution_status`/`get_execution_result`
  was not traced (UNVERIFIED, 3.2 table).
- The lifecycle reservation kind (5.1) and the `guard_reason` field are design
  proposals about the manager API; no code was written or read beyond
  `SessionGuard` and `Send`.
