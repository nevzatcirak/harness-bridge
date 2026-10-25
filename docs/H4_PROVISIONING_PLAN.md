# H4-B/C/D — Future implementation plan (not implemented or authorized)

H4-A only discovers existing ACL-authorized sessions. None of the operations below
is registered as an MCP tool. No service or authorization changes are included.

## H4-B: create_session on an existing server

Require a specific operator-registered harness endpoint, approved workspace ID,
principal and explicit create grant, distinct from prompt/read grants. Resolve
IDs through protected operator configuration; accept no caller directory as
policy. Recheck canonical root, Git storage and filesystem ownership; pin the
workspace identity before directory-scoped native POST /session. Do not use the
native project ID or a child relationship as permission.

Use a durable operation ledger before POST: principal, operation key, payload
fingerprint, approved workspace, endpoint identity and outcome. Same key/payload
returns the same mapping; changed payload conflicts. Native create may lack
idempotency. On ambiguous transport failure retain unresolved state, block replay
and require evidence/operator reconciliation. No blind second POST.

Validate returned ID, directory, native parent/project and workspace against the
request, then atomically commit an exact-session grant owned by that principal.
Dynamic grants belong in a protected versioned policy store, not repository files
or this phase's immutable maps. Parent and child grants are independent. Reads,
prompts and interrupts must reauthorize these grants on every operation. Acquire
ADR-010 writer ownership before making the new session writable. Do not restart
or weaken an existing writer to add a grant; implement a reviewed serialized
ownership/grant update transaction or keep the new session read-only.

Rollback: quarantine an ungranted/invalid result in the ledger. Never automatically
delete a native session or broaden the ACL. Explicit cleanup requires separate
authority and evidence no native work remains. Crash recovery reconciles pending
creates before granting access; a lost response is not proof of noncreation.
Acceptance: denied root/instance/parent, concurrent keys, native errors, uncertain
POST, grant-store failure, lock failure, crash between create and grant, no leak.

## H4-C: approved managed harness lifecycle

Separate the network-facing Bridge from a local, least-privilege launcher service
with a narrow authenticated local IPC interface. Launcher owns a fixed versioned
OpenCode executable profile and fixed argument templates; run as an unprivileged
workspace service UID, never root. Caller selects an approved workspace/profile
ID, not an executable, shell command, environment, port, PID or arbitrary path.
Reject command fragments, executable overrides and paths outside protected
operator-approved roots. Never offer arbitrary shell execution through IPC.

Confinement must exist at OS level: protected launch profiles; no-follow descriptor
walk/pinned directory handles; mount/filesystem sandbox restricted to the approved
workspace and explicit Git common directory; resource/process limits; private
credentials; loopback-only API; bounded startup health check. Repository config,
plugins and agent tools can execute code during harness startup/use: review or
isolate them under the workspace UID; lexical path checks alone are insufficient.
Revalidate ownership and mounts before spawn. Do not expose host secrets or inherit
Bridge's environment/credentials. Resolve Git pointers without trusting them as
new root grants. An existing endpoint is attach-only and never adopted as owned.

launch_harness uses a durable principal/profile/workspace/idempotency ledger and
per-workspace launch reservation. Return a managed opaque instance ID only after
verified health/version/workspace. Track UID, executable digest, boot ID, process
start identity, supervisor unit/cgroup and listening socket; PID alone is unsafe.
get_harness_instance returns bounded owner-filtered managed metadata and observed
health, not arbitrary process inspection. Endpoint ID from H4-A is not a process
identity and cannot authorize a shutdown.

Shutdown requires an explicit owner-scoped lifecycle grant and the exact managed
instance identity. Reconcile active/unknown executions and pending permissions;
refuse automatic shutdown while outcome is uncertain. Only signal the launcher's
owned cgroup/process handle after revalidating start identity; bounded graceful
shutdown and separately authorized escalation. Never kill an attached existing
OpenCode, unrelated PID, native session or TUI based on endpoint/title alone.
Bridge transport Close must never imply native shutdown.

On startup failure stop only the newly owned process, revoke its uncommitted
endpoint grant and record failure; retain uncertain work for reconciliation.
On Bridge restart, inspect durable ledger and supervisor identity; no automatic
relaunch or adoption. ADR-010/011 execution IDs and idempotency remain memory-only
until a separate durable execution design is accepted. Rollback requires fencing
new writes and reconciling accepted work; never start two writers. Acceptance:
path swap/symlink races, profile tampering, environment injection, PID reuse,
wrong owner, reused ports, failed health, crash/restart, uncertain shutdown.

## H4-D: Claude Code discovery and launch/resume adapter

Research baseline (official sources checked 2026-10-25):
[CLI reference](https://code.claude.com/docs/en/cli-reference) documents interactive
launch, print mode, continue in the current directory, explicit resume ID/name,
and version-dependent background-session attach behavior. Current CLI also has
`claude agents --json` for background-session inventory. Do not assume the
installed host version implements these newer facilities; none was run here.
An explicit ID resume can search other projects, so native resolution is not
an authorization boundary. Do not accept transcript file paths or ambiguous names
as resume targets in the adapter.

[Agent SDK sessions](https://code.claude.com/docs/en/agent-sdk/sessions) documents
TypeScript listSessions/getSessionMessages and Python list_sessions/
get_session_messages for local discovery/history, plus explicit resume and fork
options. Session persistence stores conversation, not filesystem snapshots.
SDK discovery does not establish ownership of a running interactive terminal.

Prototype only after separate approval and installed-version audit. Keep the Go
adapter boundary; do not add a Python/Node backend dependency without explicit
approval. A fixed native CLI print/stream-json profile is one candidate; SDK use
requires a separate dependency/runtime decision. Pin exact workspace/transcript
identity, capture session IDs and correlation, parse bounded structured events,
serialize session writers and retain native permission controls. Refuse concurrent
resume while an active writer cannot be fenced. Current documented attach of
managed background sessions is distinct from control of arbitrary active TUIs;
verify supported transport and ownership before making any control claim.
Test historical resume continuity, cwd confinement, cross-project ID rejection,
missing transcript, running-session conflicts, crash uncertainty and child lineage.

## Live deployment prerequisites

Obtain separate approval after Draft PR review; preserve H3 services meanwhile.
Build a reviewed binary, confirm installed /doc/version and the Linux confined
metadata-reader behavior under the actual service UID and approved common
directories. The H4-A reader no longer requires or invokes Git. A non-Git test workspace should correctly yield no repository.
Verify strict canonical directory policy against the current session configuration.
Use read-only staging first with unchanged roots/session grants/TLS/firewall.
For any writer replacement follow ADR-010/011 fencing, native reconciliation,
shared protected locks, retained ledger, no automatic restart and rollback.
Do not enable native creation or managed launch as part of H4-A deployment.


## H4-B review prerequisites: discovery is not authorization

Define separate capabilities before repository-wide listing: (1) metadata-only
native discovery for a principal, registered instance and approved repository/
workspace; (2) exact-session mutation grants; (3) managed creation/lifecycle
ownership. Current H4-A uses only the static ACL plus the global read-only gate;
it cannot automatically discover unlisted children or grant any new writes.

A future discovery grant may query a bounded directory-scoped native session list
with pagination. It must independently validate every candidate directory, instance
and parent link, avoid exposing denied titles/paths, and treat native project IDs
as observations. Listing must never insert candidates into the writable session
map. Grant-store transactions, revocation and writer ownership updates must be
explicitly designed/tested before enabling dynamic write access.

If Git common directories legitimately live outside workspace roots, propose a
separate read-only GitStorageRoots policy with confined handles and explicit
principal/session association. Never merge it into workspace Roots, Allows,
prompt authorization, session creation or managed process profiles. H4-A adds no
such configuration or grant. Metadata remains unavailable for those workspaces.

Read-only deployment acceptance and rollback are specified in
[H4_A_REVIEW_FIXES.md](H4_A_REVIEW_FIXES.md). A root directory replacement now
invalidates the policy's inode/device identity; do not auto-reload policy to trust
a replacement. Investigate and separately authorize any new policy lifetime.
