# ADR-022: Session lifecycle (launcher, catalog, close and archive)

Date: 2026-11-11. Status: **PROPOSED**. Design only; no production code, no
contract change, nothing deployed. Written for maintainer decision D8 (Phase 7
scope).

Numbering: ADR-022 is reserved for this decision. ADR-021 (Claude Code
adapter) is a separate record; this ADR does not depend on it, and the Claude
Code launcher is out of scope until ADR-021 is accepted.

Must preserve: ADR-010 (single writer, memory-only records), ADR-011 (late
completion), ADR-012 (stateless registry derived from an immutable operator
ACL), ADR-014 (workspace identity and bounded admission), ADR-017, ADR-018 and
ADR-020 (operator-only fenced guard release). Amends none of them. The seven
original tool shapes stay unchanged.

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
  for existing deployments). `BRIDGE_READ_ONLY=1` also removes `create_session`
  and `archive_session`.

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

The existing seven tools keep their current check
(`BRIDGE_ALLOWED_SESSIONS` ∪ sessions in `BRIDGE_ALLOWED_ROOTS` where that
already applies). Sessions under a lifecycle root are **additionally**
authorized for the seven tools, so a created session can be prompted without
being added to the static list. The union is computed per call from live native
data and current config; nothing is cached as a grant.

### 3.3 Registry (location, modes, restart behavior)

The Bridge keeps a **memory-only registry** of sessions it created, with
`{session_id, root, created_at, idempotency key hash}` and counters for the
rate limits. It lives in the single writer process (ADR-010), is guarded by the
same host-ownership rule, and is **not an authority**:

- authority comes only from section 3.2 (config + live native directory), so a
  restart loses no authorization and the registry cannot be forged by editing a
  file or by a journal replay (the project invariant: no grant restoration from
  journal history alone);
- the registry is used for idempotent replay of `create_session` and for
  counting created-per-hour; per-root `max_sessions` is counted from **native
  data** (a bounded catalog read), not from the registry, so it stays correct
  after a restart and for sessions created outside the Bridge;
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

New tool, additive; the seven existing tools are unchanged.

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
   after the window) when exceeded.
2. Resolve `root[/subdirectory]`; capture directory FileInfo (ADR-014); verify
   worktree-root containment (3.2 rule 3). The Bridge **never creates
   directories**: a missing path is `NOT_FOUND`. Cloning, worktree creation and
   `mkdir` are out of scope.
3. Same key and same input: return the recorded result (memory-only). Same key,
   different input: `IDEMPOTENCY_CONFLICT`.
4. `POST /session?directory=<canonical path>` with body
   `{title, metadata: {hb_origin, hb_key}}` (`grp:203-214`). No `permission`,
   `agent`, `model`, `parentID` or `workspaceID` is accepted from the caller:
   the Bridge never lets Chat pre-set a permission ruleset (`ses:260-269`
   accepts one) because that would sit next to the "no implicit native
   permission approval" invariant.
5. Verify the answer: the returned `directory` must canonicalize to the
   requested path and the identity must still match; otherwise the Bridge
   reports `UNKNOWN_OUTCOME` and does not use the session.
6. Optional first prompt: through the existing `send_prompt` admission with a
   derived key (`<key>:first`), so the writer, guard and replay rules of
   ADR-010/011 apply unchanged.
7. Audit and return.

### 4.2 Failure modes

| Case | Result |
|---|---|
| Root not configured, subdirectory escapes, path missing, worktree root above the root | `FORBIDDEN` / `INVALID_INPUT` / `NOT_FOUND`; no native call |
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
  writer role, and `BRIDGE_READ_ONLY` removes it. Independent processes do not
  share the registry.
- `archive_session` takes the same admission path as `send_prompt` on that
  session: it runs under the run's token, so it cannot interleave with a
  prompt, an interrupt or an ADR-020 guard release (section 6.3).
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
(`idle|running|waiting_permission|unknown|archived` mapped from the existing
ADR-012 state semantics plus an `archived` flag), `updated_since`,
`created_by_bridge` (label only), `harness`. Bounds as `find_sessions`: limit
default 50, max 200; offset 0–1000; ID-sorted; fresh view, not a snapshot; one
total deadline of 10 s; errors never return silent partial results. Ordering by
recency is a filter on `updated_since`, not a sort, to keep ID-sorted paging.

### 6.2 Aggregate across harnesses

ADR-012 states that multi-harness aggregation needs "a future versioned
instance-qualified session ID" and that the current IDs are not unique across
servers. This ADR adopts exactly that, additively: catalog entries carry
`session_ref = {harness: "harness:<hash>", session_id: "<existing id>"}`. The
existing `opencode:<native-id>` form is unchanged for the other tools and is
accepted as before when only one harness is configured. Aggregate counts
(`{total, by_state, by_root}`) are computed from the same bounded page set and
labelled with the page window; they are not whole-fleet totals. Claude Code
entries appear after ADR-021 and are omitted (not guessed) before.

### 6.3 Archive state

An `archived` flag shows in the catalog only when the native listing can show
it. The HTTP list has no `archived` parameter at v1.19.4 (`grp:30-38`), so
**archived sessions are not visible to the Bridge catalog** unless `get` by ID
works on them (**UNVERIFIED**). The Bridge therefore also records, in memory
only, the IDs it archived since startup, for audit and for the answer to "did I
archive this". That memory is lost at restart by design.

## 7. Close and archive

### 7.1 Tools

- `archive_session {session_id}`: sets `time.archived` via `PATCH`
  (`hnd:183-203`). One session per call; no bulk form, no pattern, no
  "archive all".
- `close_session`: **not a separate action.** OpenCode has no "close" other
  than abort (stop a running turn), archive and delete. Abort is already
  `interrupt_execution`. This ADR does not add a second name for it.
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

**Recommendation: (A)**, accepted by the maintainer as one-way, with the caps
below, because it adds no Bridge state and the data is preserved. If the
maintainer wants real reversibility, choose (B) and open a follow-up ADR for
the overlay's persistence. **The maintainer decides.**

### 7.3 Preconditions (both options), fail-closed

`archive_session` refuses, with no native write, unless all of these hold at
call time and again immediately before the `PATCH` (re-check under the run
token, then write):

1. the session passes section 3.2 with `archive` mode;
2. native status is `idle` and **no** Bridge execution for it is `running`,
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
  operator who needs it uses OpenCode directly, or a later operator-only command
  on the ADR-020 admin socket (fenced, audited, not reachable from the MCP
  ingress).
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
| Prompt injection drives mass archive | one session per call; `archive_per_hour` cap (default 10); precondition 7.3; archive only under configured roots with `archive` mode; audit line per call; `BRIDGE_READ_ONLY` removes the tool; cap reached returns `SESSION_LIMIT` and stops |
| Prompt injection drives mass create (runaway launcher) | `create_per_hour` (default 10), `max_sessions` per root (default 20), `max_total_sessions` (default 50), no bulk, no recursion (a created session's own tool calls count against the same budget because the caps are Bridge-wide) |
| Disk or session exhaustion | the caps above, counted from native data; no directory creation; first-prompt size bounds as `send_prompt` |
| Escape from the root (`..`, symlink, replaced directory, worktree above the root) | clean-path rule, symlink resolution, ADR-014 identity recheck, worktree-root containment (3.2) |
| Plugin execution in a new directory (F-2) | operator trust statement per root, no mkdir by the Bridge, residual risk documented (5.4) |
| Forged provenance metadata | metadata is a label only; counts and authority come from native directory and config |
| Replay of an uncertain create after restart | `UNKNOWN_OUTCOME`, no auto-retry; documented duplicate risk; caps bound it |
| Archive racing a prompt or guard release | admission through the run token, re-check before write (7.3) |
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
3. **Root allowlist with stateless derivation (chosen).** Authority is config
   plus live native directory, like ADR-012's read-through registry.

## 10. Implementation plan (PR slices)

Each slice is one task, one branch, one PR, with RED before GREEN where
practical. Mutations are the ones a reviewer should re-run.

| Slice | Content | Tests | Mutations |
|---|---|---|---|
| S1 | Lifecycle config parser and root validation (no tools) | table tests for every startup refusal in 3.1 (non-canonical, overlap, `/`, missing, wrong owner, mode) | accept an overlapping root; skip the canonical check |
| S2 | Effective authorization (3.2), including worktree-root containment | symlink, replaced-directory, worktree-above-root, root with `create` only, static session outside roots | drop rule 3; compare paths without resolving symlinks |
| S3 | `list_catalog` read-only, instance-qualified `session_ref`, bounds | limit/offset/deadline, filters, no silent partial result, archived not visible | remove the deadline; drop a filter |
| S4 | `create_session` against a fake native with `POST /session?directory=` | every row in 4.2, idempotent replay, conflict, caps, `UNKNOWN_OUTCOME` never retried, no `permission`/`agent` accepted | retry on timeout; accept a caller `permission`; cap disabled |
| S5 | Audit lines for create and archive | one line per call on every return path, no free text | skip an audit on a refusal path |
| S6 | `archive_session` | preconditions 7.3 each refuse with no native write; re-check before write; rate cap; `UNKNOWN_OUTCOME` | drop the pre-write re-check; archive while a guard is held |
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
7. Restart the Bridge: authorization of created sessions persists (derived),
   registry empty, replay of an old key is a new request (documented).
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

## 13. Limitations of this record

- Static reading of one upstream commit; behavior of an archived session on
  `get` and on prompt, and any out-of-band unarchive, are UNVERIFIED.
- No code, test or live run was done for this ADR.
- The number defaults, tool names and config keys are proposals.
- How Chat identifies "the current session" to the Bridge is UNVERIFIED, so
  precondition 7.3.5 is only enforced where the Bridge knows an active
  execution.
