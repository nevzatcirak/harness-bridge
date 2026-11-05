# F-2 residuals: workspace routing for status and permission (design note)

Status: proposal, not implemented. Task F2-WORKSPACE-ROUTE-VERIFY (issue #49),
2026-11-05, Worker C. Residuals from R3-PR45-REVIEW (issue #46):

- P2: the `send_prompt` preflight still reads directory-scoped status and
  permission for workspace-bound sessions.
- P3: discovery now reports every workspace-bound session `unknown`, even when the
  directory instance does serve it.

Static source reading only; OpenCode was not run and no live call was made.

Upstream `anomalyco/opencode` tag v1.19.4, commit
`148842ef7cb1bc34192b3fff57d0f9721ab9c801`. Path abbreviations below:

- `wr.ts` = `packages/opencode/src/server/routes/instance/httpapi/middleware/workspace-routing.ts`
- `shared.ts` = `packages/opencode/src/server/shared/workspace-routing.ts`
- `cp/` = `packages/opencode/src/control-plane/`
- other paths are relative to `packages/opencode/src/`

Anything not settled by source is marked **UNVERIFIED**.

## 1. How workspace routing works

Every instance route passes through `WorkspaceRoutingMiddleware`, then
`InstanceContextMiddleware`, which loads (bootstraps) the chosen directory
(`wr.ts:212-236`; `server/routes/instance/httpapi/middleware/instance-context.ts:23-35`).

**Request plan** (`wr.ts:160-184`):

1. The workspace ID is the session's `workspaceID` when the path names a session
   (`shared.ts:20-29`, `wr.ts:221-231`); otherwise it is the `?workspace=` query
   (`wr.ts:69-71`). `/session/status` never resolves a session (`shared.ts:21`).
2. A workspace ID that is not in the local database gives HTTP 500
   `Workspace not found` (`wr.ts:102-106`, `:173-175`).
3. A known workspace is used **only if the route is not control-plane local**
   (`wr.ts:177-179`, `:90-92`). The rules (`shared.ts:5-18`) are:
   - `/experimental/workspace*` → local;
   - `/session/status` → forward;
   - **every `GET /session…`** (including `/session/:id` and
     `/session/:id/message`) → local;
   - everything else (including `/permission`, `POST /session/:id/prompt_async`
     and `/abort`) → forward.
4. Forwarded to a workspace: the adapter `target()` decides
   (`wr.ts:148-157`, `cp/workspace-adapter-runtime.ts:14-19`):
   - `local` → `Local({directory: target.directory, workspaceID})`; that directory's
     instance serves the request.
   - `remote` → HTTP proxy to `target.url`, with the `workspace` and `directory`
     query parameters removed (`shared.ts:31-45`). It requires an active sync
     connection, otherwise HTTP 503 `broken sync connection` (`wr.ts:113-145`).
     No local instance is loaded on this path.
5. Not forwarded: `Local({directory: session.directory || ?directory= ||
   x-opencode-directory || cwd})` (`wr.ts:181-184`, `:86-88`).
6. If `OPENCODE_WORKSPACE_ID` is set, the server is itself a workspace and does not
   forward (`wr.ts:65-67`, `:96-99`, `:177`).

**Correction to H4A-F2-VERIFY (PR #40) and CONTRACTS.md (PR #45).** Those
documents say that `GET /session/:id` routes a workspace-bound session to its
workspace target. That is wrong. By rule 3, `GET /session/:id` and its history
stay local and load `session.directory`. So since H2 the Bridge's `authorized()`
lookup and `read_session_history` already boot `session.directory` locally for
workspace-bound sessions too. PR #45's skip does not remove that bootstrap. It
only stops directory reads whose result does not describe the session. Only
non-GET session calls (prompt, abort) and `/session/status` / `/permission` with
`?workspace=` reach the workspace target.

## 2. Workspace types

The built-in adapter is `worktree` (`cp/adapters/index.ts:5-7`): a git worktree
whose `target()` is always `local` with the workspace's `directory`
(`cp/adapters/worktree.ts:89-95`). Plugins can register further adapters per
project, and a custom adapter **overrides a built-in of the same type name**
(`cp/adapters/index.ts:11-19`, `:37-41`; `plugin/index.ts`, where
`experimental_workspace.register` is passed to plugins). Targets are `local
{directory}` or `remote {url, headers}` (`cp/types.ts:30-39`).
`Workspace.Info` has `type`, `directory` (nullable), `branch`, `extra` and
`projectID` (`cp/types.ts:7-15`).

| Class | How it arises | `session.directory` vs served instance | Directory read correct? |
|---|---|---|---|
| A. Local worktree, session created in it | `POST /session` routed to the workspace. `Session.create` stores the instance directory and the route's workspaceID (`session/session.ts:667-688`; `effect/instance-state.ts:20-21`). | equal (`target.directory`) | **yes** (the P3 case) |
| B. Local, session warped (moved) in | `Workspace.sessionWarp` → `Session.setWorkspace` patches only `workspaceID` (`cp/workspace.ts:633-640`, `session/session.ts:814-820`). | `session.directory` stays the original; the target is the workspace directory | **no** |
| C. Remote (plugin adapter) | Created or warped into a remote target; requests are proxied | the local directory instance never runs the session | **no** |
| D. Unknown | Missing workspace row, an adapter not loaded (`getAdapter` throws, `cp/adapters/index.ts:18`), `directory` null | — | **unknown** |

Workspaces are gated by `flags.experimentalWorkspaces`. With it off, remote sync
never starts (`cp/workspace.ts:440-442`), so a remote target answers 503; local
targets do not need sync (`:457-460`). **UNVERIFIED:** whether workspace rows and
session `workspaceID`s are created at all with the flag off, and the value of
`session.directory` synced into the local database for remote sessions. If it is
a path that does not exist locally, `Policy.Allows` already rejects the session.

## 3. Does `?workspace=<id>` route `/session/status` and `/permission`?

Yes. Both routes are forwarded (rule 3) and carry no session ID, so the
`?workspace=` query selects the workspace (`wr.ts:69-71`, `:160-179`):

- **Local target:** answered by the instance for `target.directory`, carrying the
  workspaceID. This is the instance that runs the session's prompts, so the
  status map and pending permissions are the right ones. If that instance is not
  yet loaded, it is **bootstrapped**, plugins included (F-2). In class A it is the
  same instance `GET /session/:id` already loads. In class B it is the worktree
  directory, which the Bridge has not otherwise loaded.
- **Remote target:** proxied to the remote server (or 503 without sync), with no
  local bootstrap. Plugins run on the remote host, not the Bridge host.
- **Unknown ID:** HTTP 500 `Workspace not found`. **Unresolvable adapter:**
  **UNVERIFIED** status code (target failure inside `planWorkspaceRequest`). In
  both cases the Bridge sees a non-2xx response and fails closed.

## 4. Can the Bridge classify a workspace from metadata it can read?

- **From the session alone: no.** `Session.workspaceID` is just an ID.
- **With the workspace record: partly.** `GET /experimental/workspace?directory=<session.directory>`
  is control-plane local (`shared.ts:6`) and lists `Workspace.Info`
  (`server/routes/instance/httpapi/groups/workspace.ts:12`, `:63-72`, `:131-133`).
  It loads `session.directory`, which `GET /session/:id` has just loaded, so it
  adds no bootstrap. The Bridge can then:
  - **Positively identify class A:** `type == "worktree"`, a non-null
    `directory` equal to the canonical `session.directory`, and the same project.
    One caveat: a project plugin may override the `worktree` type with a remote
    adapter, which the type string cannot reveal (**UNVERIFIED** whether any
    deployment does this).
  - **Identify class B:** a local-looking record whose `directory` differs from
    `session.directory`.
  - **Not distinguish** class C from class D by type alone.
- **Authoritative but wider: routing.** A `?workspace=` read makes OpenCode
  itself pick the instance that serves the session, so it is correct in classes
  A–C by construction. The price is a possible bootstrap of `target.directory`
  (class B) and a 503/500 in classes C/D.

## 5. Proposed design

Principle: never derive a workspace-bound session's state from an instance that
may not serve it. Never cause OpenCode to bootstrap a directory outside the
Bridge ACL roots. When in doubt, report `unknown` (discovery) or refuse
(`send_prompt`).

### 5.1 Workspace resolution (shared helper)

For a session with a non-empty `workspaceID`, read
`GET /experimental/workspace?directory=<session.directory>` (no new bootstrap) and
select the record with that ID. Then classify:

- **Eligible-local:** `type == "worktree"`, `directory` non-null,
  `Policy.Allows`-style canonical check of `directory` within
  `BRIDGE_ALLOWED_ROOTS` (absolute, clean, no symlink, under a root), and
  `projectID` equal to the session's `native_project_id`.
- **Not eligible:** anything else, including a missing record, a non-2xx
  response, a custom type, a null directory or a directory outside the roots.

Cache per request only. Do not persist (non-atomic, like all native evidence).

### 5.2 Discovery (`list_sessions`, `find_sessions`, `get_session`, repository tools)

| Case | Reads | State |
|---|---|---|
| No workspace | unchanged: `?directory=` status and permission | unchanged (FIX-STATE-01/01b) |
| Eligible-local, `directory == session.directory` (class A) | `?workspace=<id>` status and permission (loads the same instance as the GET) | full mapping, including absence → `idle`, because this instance runs the session |
| Eligible-local, `directory != session.directory` (class B) | `?workspace=<id>` status and permission; may bootstrap the in-roots worktree directory | full mapping. Alternative, if the operator wants no extra bootstrap: `unknown` |
| Not eligible (C/D) | none | `unknown`, no `native_state` (current PR #45 behaviour) |

- Any non-2xx or malformed response gives `unknown`.
- The Bridge guard still applies last.
- `get_harness_capabilities` keeps returning `INVALID_INPUT` for workspace-bound
  sessions. `/agent` with `?workspace=` would be routed the same way, but no task
  needs it.

### 5.3 `send_prompt` preflight (`internal/execution`, P2)

Today the preflight reads `?directory=` status and permission
(`internal/execution/manager.go`, `Status`/`Permissions` via the directory-scoped
runner). For a workspace-bound session that instance may not run the prompt (B,
C), so "not busy" there proves nothing.

Proposal:

- **Not eligible (C/D):** refuse before any native mutation, with a typed error
  (`INVALID_INPUT`, `retryable:false`, "send_prompt is unavailable for
  workspace-bound sessions whose workspace cannot be verified"). No `?directory=`
  status or permission read.
- **Eligible-local (A, B):** route status and permission with `?workspace=<id>`.
  The prompt itself (`POST …/prompt_async`) is already forwarded to the same
  target by OpenCode. History and correlation reads (`GET /session/:id/message`)
  stay local and read the local database. That database is shared for local
  targets.
- **Interim option (smallest change, also fail-closed):** refuse every
  workspace-bound `send_prompt` until 5.1 exists. This removes P2 entirely at the
  cost of the feature.

Either option changes `internal/execution` semantics. It needs its own task,
RED/GREEN tests with a recording upstream, and an independent review.

### 5.4 Fail-closed rules

1. No workspace-derived state without a successful, well-formed workspace
   record. Read errors, an unknown ID or a 500/503 give `unknown` (discovery) or
   refusal (`send_prompt`).
2. Never route to a workspace whose `directory` fails the ACL root check, so that
   OpenCode never bootstraps a directory outside the ACL. Never route to custom
   adapter types. Remote targets are never contacted through the Bridge.
3. Never send `?workspace=` for a session without a `workspaceID`, and never send
   a client-supplied workspace ID. The ID comes only from native session
   metadata of an ACL'd session.
4. A workspace row is untrusted native metadata (it can be changed by plugins and
   other clients), like everything else the Bridge reads. Re-check it on every
   request and keep it non-authoritative for authorization. The ACL grant
   remains per session.

### 5.5 Effect on the F-2 bootstrap surface

- **Unchanged:** `session.directory` of every ACL'd session is bootstrapped by
  the ACL lookup (`GET /session/:id`), as since H2.
- **Added, class B only:** the in-roots worktree directory of a workspace into
  which an ACL'd session was moved. That is the instance that runs the session's
  prompts anyway, and rule 2 keeps it inside the ACL roots. If even that is
  unwanted, choose `unknown` for class B (table above).
- **Removed:** directory reads that described the wrong instance (P2, and the
  read side of P3).
- **Remote:** never a local bootstrap; the Bridge does not route to remote targets.

## 6. Open points (UNVERIFIED)

- Whether workspace rows and session `workspaceID`s exist with
  `experimentalWorkspaces` off. Also the HTTP status for an adapter whose
  `target()` throws.
- The synced local `session.directory` and message history for remote-workspace
  sessions. If the directory does not exist locally, `Policy.Allows` rejects the
  session today.
- Whether any project overrides the `worktree` adapter type with a remote one.
- Whether `/experimental/workspace` lists workspaces of other projects. The design
  filters by `projectID` either way.
- Live behaviour of all of the above. Settling it needs a disposable OpenCode
  1.19.4 with `OPENCODE_EXPERIMENTAL_WORKSPACES` and a test worktree workspace,
  not the R5 instance.

## 7. Recommendation

1. Merge this note and the CONTRACTS.md correction (section 1).
2. For R5 (no workspace-bound session in the ACL): no code change is required.
   P2 and P3 cannot occur with the current single ACL'd session, whose
   `workspaceID` is empty (H4_A_EVIDENCE.md).
3. Before any workspace-bound session is ACL'd, implement the interim refusal in
   `send_prompt` (5.3) as a small `internal/execution` task. Then 5.1/5.2 (class
   A/B state via `?workspace=`) as a separate discovery task, after a disposable
   live check of section 6.
