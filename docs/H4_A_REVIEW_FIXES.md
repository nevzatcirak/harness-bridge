# H4-A security and discovery review fixes

Date: 2026-10-25. Implementation on Draft PR #4 only; no deployment performed.
Before editing, fetched origin and verified local HEAD, remote H4 head and PR #4
all at `eb46a6081d40687611539d9834a8bbd0c18b8d08`. PR #4 remains stacked on
`feat/h3-secure-claude-remote-connector` (H3 PR #3); the fetched H3 head was
`4cb09fb6243fcc787ae2fe08c17c367fbd06fdf7`. No H3/deployment PR was edited.

Read the specification, accepted ADR-010/011, proposed ADR-012/013, original
H4_A_EVIDENCE and H4_PROVISIONING_PLAN before corrections. This document supersedes
the original H4-A Git subprocess inspection mechanism and discovery cost model.

## Findings and resolutions

| Finding | Correction | Scope |
| --- | --- | --- |
| Git followed storage pointers and parsed local config before returned paths were authorized | Remove Git execution; authorize each required storage path before a confined file read | No repository config/includes/remotes are opened |
| Environment sanitization did not disable repository config includes | No Git process or config parsing exists in the metadata reader | Hooks, filters, helpers, Git environment and network commands cannot be invoked by this reader |
| A canonical/lexical check alone was vulnerable to path replacement | Go os.Root confinement, policy root inode/device pin, observed symlink rejection, no-follow/nonblocking Linux opens | Confines metadata resolution, not native harness filesystem activity |
| Special or linked files could expose unrelated data or block reads | Require regular singly linked files; bounded reads; compare file identity before/after open | FIFO HEAD and hardlinks rejected; FIFO config proves config is ignored |
| Replacement after authorization could produce stale identity | Recheck observed identity/size/mtime and policy root identity; suppress unsafe metadata | Best effort, not an atomic snapshot |
| Opaque hashes made repository selection difficult | Optional printable display_name for repository/workspace; retain branch/HEAD; add linked_worktree | Names limited to 128 Unicode characters; HEAD is validated 40/64-digit hex |
| Repeated state and Git queries scaled with sessions sharing a workspace | Request-local directory grouping; reuse native state/permissions and metadata; reuse validated parent metadata | No cache between requests; retain only each directory's authorized native state entries |
| Discovery work was serial and unbounded across callers | Four workers/request, two admitted requests/Server, 10s deadline including admission queue | At most eight active discovery jobs/Server; <=1000 ACL entries |
| History continuation direction was not documented | Live ordering audit plus fixture test; native array order retained | No implicit sorting, reversal, synthetic cursor or change to unpaginated history |
| Discovery could be mistaken for write/lifecycle authority | Explicit discoverable/writable/managed definitions and H4-B prerequisite separation | Static ACL remains authoritative; no dynamic grants or session creation |

The reader recognizes only a bounded subset of Git's files backend: ancestor
`.git` directory/file, optional `commondir`, `HEAD`, direct loose ref or packed-refs.
Pointer/HEAD/loose-ref reads are at most 4 KiB; packed-refs at most 1 MiB; ancestor
search at most 128 steps, confined to existing approved roots. A cooperative 2s
inspection budget is checked around filesystem operations. Detached/unborn HEAD
is supported; indirect symbolic refs, bare repositories, reftable and config-defined
custom layouts have no general compatibility guarantee. Detected reftable storage
and unsafe/inaccessible layouts yield no repository metadata. Non-Linux platforms
fail closed for this reader. Session visibility/history still work without Git
metadata; metadata errors never grant access or silently invent a repository.

No remote URL is read or returned, so credentials embedded in upstream URLs cannot
leak through these display fields. Display names are local path-derived hints,
not upstream identity, policy or executable instructions. Repository/workspace
hash formulas remain unchanged for supported normal and linked worktree layouts.

## Discovery and error behavior

For N authorized IDs in D validated canonical directories, native discovery uses
N metadata GETs plus 2D state GETs. Per-directory repository inspection is also
shared. Parents must belong to the same independently validated static ACL result;
final path validation filters both returned sessions and visible parent links.
Metadata GET forbidden/not-found results are omitted, other metadata failures
fail the request; native state/permission errors return unknown. Deadline or caller
cancellation fails the whole discovery, including admission queueing, with no
partial session list. Selection filters and bounded pages run on the ID-sorted
visible result. No result claims atomic native state or stable membership across
pages. The 8 MiB native-response bound remains.

`list_sessions` retains its no-input success shape and all old required fields;
it now uses the same bounded pipeline, explicitly failing ACLs over 1000 entries
or expired deadlines. History without pagination retains its messages payload and
query behavior. The eleven-tool surface retains all original seven tool names,
required inputs and execution/idempotency contracts. MCP descriptions recommend
repository/session selection first, explicit IDs for mutations, 20–50-message
history pages only as needed, execution evidence rather than transcript copying,
and conditional native subagent delegation. No Chat spawning API is assumed.

## Fixture evidence

Final required commands, all successful:

```
gofmt -w cmd internal
go build ./...
go vet ./...
go test ./... -count=1
go test -race ./... -count=1
git diff --check
```

Final unit/integration package times: cmd/bridge 0.022s; OpenCode adapter 0.159s;
execution 0.185s; MCP 2.199s; repository 0.188s; security 0.014s.
Final race package times: cmd/bridge 1.070s; adapter 1.276s; execution 2.366s;
MCP 6.507s; repository 1.299s; security 1.030s. Other command/client packages
have no test files and build successfully.

A dedicated fixture scale run (same non-Git directory, no network delay) measured:

| Sessions | Metadata GETs | Status GETs | Permission GETs | Elapsed |
| --- | --- | --- | --- | --- |
| 1 | 1 | 1 | 1 | 2.68 ms |
| 50 | 50 | 1 | 1 | 5.09 ms |
| 200 | 200 | 1 | 1 | 17.33 ms |
| 1000 | 1000 | 1 | 1 | 76.43 ms |

These are fixture observations, not latency guarantees or live OpenCode load tests.
Tests assert call counts/bounds, not wall-clock performance. Additional tests
isolate evidence across two different directories, check cancellation before
state reads and during admission, reject an oversized ACL before contacting
native APIs, and bound returned find_sessions pages to 200.

Repository tests cover external .git pointers, unapproved common storage,
linked-worktree-only grants, approved linked storage, actual independent local
clones, packed refs, non-Git roots, display bounds, credential non-disclosure,
ignored hostile config/environment, symlink HEAD/.git, root/file replacement,
FIFO/hardlink rejection, a missing-target symlink ref (never mistaken for
an unborn branch), and a component swap *after acquiring a confined handle*.
That last test directly verifies the escaped open fails, rather than relying only
on a post-read path check. Concurrent identity/discovery tests and SDK integration
verify race safety, static ACL isolation, the original seven tool contracts, all
eleven tools, read-only guards, and unchanged legacy history with cursor ordering.

Intermediate development checks caught a platform stat-type mismatch in the
new regular-file helper; it was fixed using descriptor Fstat before the passing
final full runs. No failing check is represented as success.

## Actual OpenCode observations (GET only)

Only approved test `127.0.0.1:4097`, exact existing session
`ses_e9329c0b4ffebOlm7GRyKxbMQ5`, and existing approved cwd were accessed using
the existing test credential without printing it. GET /global/health still reports
healthy=true/version=1.19.4. Root server :4096 was not contacted.

A no-cursor `limit=3` history GET returned three messages with increasing native
created timestamps. Its opaque X-Next-Cursor returned three earlier messages,
also chronologically ordered. Comparing IDs/timestamps against one 16-message
unpaginated read showed first page == last three baseline messages and next page
== the preceding three; the windows did not overlap. Only counts, IDs and
timestamps were inspected, not transcript content. No history was changed.

This establishes selection and direction for the observed installed version and
session, not a snapshot during writes or a guarantee for future versions. Clients
must inspect timestamps and follow returned cursors; do not infer that an arbitrary
page is necessarily the latest. Fixture tests mirror this behavior and remain
separate from the live observation. No live prompt, native session creation,
permission response, native abort or child delegation was performed.

## Authorization and H4-B prerequisites

- Discoverable: existing static session ACL plus current workspace validation
  permits metadata/history visibility.
- Writable: additionally permitted by the Bridge mutation mode and existing
  writer, execution and native permission controls. H4-A has no per-session
  discovery-vs-write grant split; listing cannot broaden the static ACL.
- Managed: separately approved creation/lifecycle ownership. No H4-A session or
  server is adopted as managed.

H4-B must design metadata-only repository-wide native listing grants, validate
and filter every candidate, then design durable, separately authorized exact-
session write grants, revocation and writer-lock transitions. Proposed
GitStorageRoots would be read-only metadata storage authority associated with an
approved principal/session. It must never feed prompt/session creation/workspace
or process-launch authorization. No new roots/configuration/grants are enabled
here; external storage leaves metadata absent. See H4_PROVISIONING_PLAN.

## Residual risks and compatibility

os.Root confines path resolution but is not a mount namespace or a native-harness
sandbox. Privileged mount changes, an attacker controlling the service UID/root
policy, ordinary in-place concurrent metadata changes and arbitrary native API
writers are not fenced. Rechecks cannot prove an atomic Git/native snapshot.
Context cancellation cannot interrupt a blocked kernel/filesystem syscall; use
trusted local storage and OS resource/isolation controls. Root identity checks
fail closed after root replacement; do not auto-refresh them to trust new storage.
Unsupported layouts may lose optional metadata but retain session functionality.
Path/hostname/endpoint reuse remains a locator-identity limitation, not ownership.

No H3 listener, service, binary, ACL, credential, firewall, Traefik, DNS or Docker
configuration changed. Existing mutation and reconciliation code was not changed.
Actual H4 deployment must separately check Linux/Go 1.25 compatibility, permitted
filesystem metadata reads under the service UID, supported Git layouts, canonical
paths and <=1000 static sessions. Any writer replacement still requires ADR-010/
011 fencing, reconciliation, shared ownership locks and explicit maintenance
approval; process-local execution IDs/keys are not recoverable after restart.

## Recommended read-only acceptance and rollback (not performed)

1. Obtain separate deployment approval for an isolated read-only candidate,
   reviewed binary and operator-chosen loopback ingress. Record existing H3/
   OpenCode process start identities and configuration checksums without secrets.
   Keep current roots/session ACL, credentials, TLS/routing and writer unchanged.
   Do not add a Git storage root just to populate metadata. Validate service-UID
   local filesystem access and expected non-Git/unsupported-layout behavior.
2. With the candidate explicitly read-only, initialize MCP and require eleven
   tools including the original seven. Check repository display/IDs, multiple
   authorized candidates, parent filtering and unknown state semantics; denied
   IDs/repositories must remain invisible. Use only existing ACL sessions and
   available fixtures. Read a 20-message history page, inspect timestamps and
   follow at most one cursor if necessary; fetch detailed execution evidence
   only for a known retained record. Do not replay old execution keys.
3. Confirm the read-only gate denies send_prompt/interrupt before any native
   mutation (use instrumented fixtures, or a separately approved denied live
   request). Verify no native POST/permission response, new native sessions,
   history mutation or change to original H3/OpenCode process identities. Observe
   bounded cancellation/error behavior and no credential/remote/transcript logs.
4. On acceptance failure, withdraw only the isolated candidate through the
   approved maintenance action, retain sanitized evidence and keep the existing
   H3 service running. Do not restart OpenCode or change grants. If a future
   approved replacement touches a writer, fence/reconcile accepted native work
   before stopping/restoring it; restore the previous reviewed H3 configuration,
   never two writers, and never automatically replay prompts or adopt a process.
