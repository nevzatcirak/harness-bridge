# H4-A — Implemented behavior and feasibility audit

Date: 2026-10-25. Feature branch only; no deployment, native prompt/session
creation, native abort, permission response, credential rotation, installation,
service restart or infrastructure change. Original root :4096 was not contacted.
Installed /doc SHA-256:
`eebb56042880e07c354e0ad0a31f70aab45f536adf5b40b6b1fd5abf4663417b`.
Read HARNESS_BRIDGE_SPEC.md, architecture ADR-001–009 and accepted ADR-010/011
before implementation. H4 PR is stacked on the existing H3 feature branch.

## Live read-only observations

Only approved non-root test OpenCode `http://127.0.0.1:4097` was queried, using its
existing credential without printing it. Initial sandbox loopback access failed;
approved external read access succeeded. An unauthenticated /doc and a credential
for the other service returned no usable schema; only the existing test credential
was valid. No authentication configuration changed.

| Audit item | Installed evidence | H4-A behavior |
| --- | --- | --- |
| Version | GET /global/health: healthy=true, version=1.19.4 | No binary installed/run |
| API schema version | /doc info.version=1.0.0 | This is not executable version |
| Project | /project/current for approved cwd: id=global, worktree=/ | Native labels are not repository identity |
| Session metadata | Exact existing approved session: same cwd, projectID=global, version=1.19.4; no parentID/workspaceID | Optional native fields retained |
| Session list | GET /session?directory=approved-cwd&limit=5 returned one existing session | Registry uses ACL Gets, not a global list |
| List filtering | /doc: directory,workspace,scope,path,roots,start,search,limit | No claim each filter was exercised |
| Status | Directory-scoped /session/status returned {} | Absence means unknown |
| Permissions | Directory-scoped /permission returned [] | No automatic replies |
| Children | GET /session/{approved-ID}/children returned [] | Parent metadata supported; real child lineage not exercised |
| Visible primary agents | /agent: build, plan | Live inventory, hidden agents omitted |
| Visible subagents | /agent: explore, general | Names do not prove effective delegation permission |
| Creation | /doc declares POST /session and parentID in Session schema | Native declaration only; Bridge creation unsupported; no POST performed |
| History bounds | limit=2 returned two messages | Bridge checks requested count |
| History cursor | Message ID as before returned 400; X-Next-Cursor opaque token succeeded | next_cursor exposed unchanged; before accepts opaque token |

Installed create request schema allows optional parentID, title, agent, model,
metadata, permission and workspaceID, with directory/workspace query routing.
These fields are audited declarations, not H4-A inputs; native creation permission
semantics and rollback are untested.

Two live history pages each contained two messages with no overlapping IDs.
Only ID/count/metadata summaries were inspected; raw history and the full schema
remain temporary local audit files and are not committed. Legacy history uses an
array, unlike newer v2 client cursor envelopes. Default legacy history calls
remain compatible, including original messages key and payload.

## Installed API versus current v2 documentation

The installed /doc contains both legacy `/session`, `/agent`, `/permission`,
`/project` and newer `/api/session`, `/api/agent`, `/api/permission/...` routes.
Presence does not prove their operational equivalence. H3 and H4-A use only the
verified legacy surface. Installed v2 message listing declares limit/order/cursor,
history declares limit/after; legacy message listing declares limit/before and
returns the continuation in a header.

The [official v2 client/plugin documentation](https://opencode.ai/v2/docs/build/plugins/)
shows location/project context, session listing with cursor.next, context reads,
prompt/interrupt operations and session-scoped permission access. These are a
different contract from legacy async prompt/status/abort and global permission
listing. No mechanical endpoint substitution, v2 migration or SDK dependency was
introduced. Native project metadata remains distinct from locally inspected Git.

The [v2 agent configuration](https://opencode.ai/v2/docs/agents) also warns that
legacy top-level configuration fields differ. H4 reads inventory only; it neither
rewrites native config nor infers effective delegation authority from agent mode.
Claude Code official research and future adapter restrictions are in
[the H4-B/C/D plan](H4_PROVISIONING_PLAN.md).

## Implemented capability contract

Four additions, all read-only: list_repositories, get_repository, find_sessions,
get_harness_capabilities. Session outputs gain optional repository/workspace,
parent_session_id, harness_instance_id, native project/workspace/state metadata.
The seven existing tool names, required inputs, output keys, authorization,
idempotency and execution lifecycle stay intact. Stricter canonical workspace
checks reject aliases before upstream history/mutation. No selected-session state.
MCP initialization includes native delegation guidance.

Capabilities distinguish endpoint declarations from executed operations via their
explicit evidence string. native_session_creation=true means POST is in installed
/doc; bridge_session_creation=false always. child_session_inspection reports the
installed read endpoint declaration; authorized parents can be inspected through
existing get_session/history and find_sessions parent filtering. H4 does not call
native children listing to expand discovery. native_delegation=unknown until
actual effective permission/delegation can be verified; large-task guidance is
conditional. No independent orchestration, native spawn, dynamic ACL, launch,
shutdown or Claude Code adapter exists.

Repository identities omit remote URLs. Independent clones differ; linked
worktrees share common-directory repository identity and retain distinct workspaces.
A non-Git session is still a session and has no fabricated repository. Host/path
and endpoint identities are locators, not process-lifetime or ownership proofs.

## Verification

Deterministic tests cover real temporary Git repositories/worktrees, branch/HEAD,
independent repositories, symlink/traversal rejection, canceled inspection,
concurrent identity/discovery, unauthorized workspace/session/parent suppression,
multiple sessions and worktrees, exact repository/parent filtering, bounded pages,
opaque history cursor forwarding, unavailable/unknown/busy/retry/idle/permission
states, hidden agent filtering and capability ACLs. SDK tests traverse actual
loopback MCP transport, validate all output schemas, retain original seven tools,
exercise new discovery tools and read-only guards. H3 lifecycle/security suites
continue to run unchanged except fixture support and expected additive tool count.

Final quality results: gofmt on changed Go sources and git diff --check clean;
`go build ./...`, `go vet ./...`, `go test ./...` and `go test -race ./...` all PASS.
MCP package: 2.241s unit/integration, 5.511s race; all other packages also passed.
Intermediate tests caught an unused test variable during cursor refinement and
an intentionally blocked cancellation fixture session in the new discovery test;
both test defects were corrected before this successful full final run.
Synthetic fixtures are not a live Claude Chat H4 test.
