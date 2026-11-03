# ADR-012 — Repository-Aware Session Registry

Date: 2026-10-25. Status: proposed for H4-A review; implemented on feature branch,
not deployed. Preserves accepted ADR-010 and ADR-011.

## Decision

Use a stateless, read-through registry derived exclusively from the immutable
operator session ACL. Revalidate each native session ID and canonical directory
before inspecting local Git metadata. No mutable selected session, filesystem
crawl, repository registration, ACL expansion or database. Four read-only tools
are justified: repository listing and lookup, filtered session selection, and
native capabilities. Existing seven tools and mutation semantics remain.

| Identity | Encoding and scope |
| --- | --- |
| Repository | `repo:` + SHA-256(hostname, canonical Git common directory), NUL-delimited |
| Workspace | `workspace:` + SHA-256(repository ID, canonical worktree root, canonical Git directory) |
| Harness instance | `harness:` + SHA-256(configured loopback endpoint); endpoint identity only |
| Session | Existing `opencode:<native-id>` contract; scoped to this configured harness |
| Execution | Existing process-local execution ID and pinned native session/message/workspace |
| Parent relationship | Native `parentID`, exposed as session ID only after independent parent authorization |

Different worktrees share repository identity and retain distinct workspace IDs.
Independent clones remain distinct even if their remotes and commits match.
Remote URL is deliberately omitted: optional identity evidence is unnecessary
here and can contain credentials. Branch and HEAD are observations, not identity.
Git common-directory path changes or hostname changes change repository identity;
path reuse can reuse identity. These are local storage locators, not persistent
UUIDs or proof of ownership. Endpoint aliases may produce different harness IDs;
process replacement at the same endpoint retains the ID. Multi-harness aggregation
requires a future versioned instance-qualified session ID; do not reinterpret the
seven-tool contract or claim current IDs are unique across arbitrary servers.

Non-Git sessions remain visible without fabricated repository/workspace metadata.
An OpenCode `projectID` such as `global` is retained as native metadata and is not
a Git repository identity. Git root, common directory and worktree Git directory
must all independently fall inside operator-approved roots before being exposed.
A linked worktree whose common directory is outside those roots has no repository
metadata. A future
metadata-only storage grant must be distinct from mutation/workspace grants; it
is proposed below and is not enabled in H4-A.

## Bounds and security

`find_sessions`: ID-sorted, exact repository/workspace/parent filters, limit default
50, maximum 200; offset 0–1000; optional next_offset. ACL maximum 1000 for new
registry tools; total discovery deadline 10s. Repository lists deduplicate these
bounded authorized results. Offset pagination is a fresh view, not a snapshot:
concurrent native metadata changes may reorder membership. Errors do not silently
return partial discovery. Old list_sessions retains its original no-input shape.

Review correction (2026-10-25): Git subprocesses are removed. Sanitizing Git's
environment did not prevent repository-local config/includes and storage-pointer
reads before authorization. Inspect only the files-backend layout: .git pointer,
commondir, HEAD, direct loose ref or packed-refs. No config (including remote URLs),
objects, hooks, filters, includes, Git environment or external program is consulted.
Unsupported/reftable/bare layouts and unsafe storage yield absent metadata.

Storage paths are checked against the existing session/root ACL before opening;
Go os.Root handles confine subsequent reads during component replacement races.
Approved root inode/device identity is captured by policy construction and
rechecked on open. Observed symlink components are rejected; Linux file opens
use no-follow/nonblocking and require regular, singly linked files. Reads are
bounded to 4 KiB for pointers/HEAD/loose refs, 1 MiB for packed-refs, 128 ancestor
steps and a cooperative 2s inspection context. Identity/size/mtime rechecks detect
observed replacement. This is not an atomic filesystem snapshot or protection
against privileged mount changes; OS least-privilege confinement is still needed.
Blocked filesystem syscalls cannot be interrupted by a context deadline. Other
platforms leave repository metadata unavailable rather than using a weaker reader.

Repository and workspace display_name and workspace branch are optional,
printable and limited to 128 Unicode characters. HEAD is validated 40/64-digit
hex, linked_worktree distinguishes a common directory from a worktree Git directory.
Display fields are untrusted local observations; they are not remote identities or
authorization. Existing path/hash ID formulas are unchanged. No remote URL is read
or returned. Config-defined custom layouts are not interpreted.

All discovery/list_sessions calls share the registry pipeline. Per invocation,
metadata GETs run with at most four workers; validated parent links reuse these
results. Status, permission and repository metadata are shared only within the
same canonical directory and configured instance in that invocation. No cache
survives the request. At most two discoveries per Server are admitted; queueing
consumes the same 10s deadline, bounding active discovery jobs to eight. Work is
O(N + D) native calls (N session metadata GETs, 2D state GETs). Native state read
errors yield unknown; missing/forbidden metadata is filtered; other metadata
errors or deadline/cancellation fail the whole request without partial results.
Observed state is not atomic. list_sessions keeps its original input/output
shape but now shares the 1000-entry/10s guard; oversized ACLs fail explicitly.

Session state uses native status and permissions: explicit idle -> idle,
busy/retry -> running, pending matching permission -> waiting_for_permission;
absent/unrecognized/unavailable status -> unknown. Failed permission reads force
unknown. Native raw state is optional. This is a non-atomic observation and never
completion proof; execution correlation remains ADR-011's responsibility.

History optionally uses native limit (1–200) and opaque before cursor (maximum
2048 URL-safe characters). Preserve the native X-Next-Cursor as next_cursor;
never interpret it as a message ID, fetch an upstream Link URL, or infer EOF
from a full page. A missing cursor means no further cursor is advertised. Omitted
pagination preserves old history behavior (still subject to the 8 MiB transport
cap). Reject an upstream response exceeding the requested message count.

## Discovery, writes and management

Discoverable means static session ACL membership plus current canonical workspace
validation permits metadata/history visibility. Writable additionally requires
mutations enabled on this Bridge and the existing execution/writer/permission
checks; discovery grants nothing. H4-A has no separate per-session read-only vs
write grants: its existing static ACL and global read-only mode are authoritative.
Managed means Bridge owns separately approved creation/process lifecycle. No H4-A
session is adopted as managed and no creation/dynamic write grant exists.

H4-B must introduce distinct explicit metadata-discovery grants before using a
repository-wide native list, then validate every candidate's workspace/instance
and filter output. Proposed read-only GitStorageRoots would authorize only the
confined metadata reader for an approved session; they must never feed Allows,
prompt admission, creation or managed launch. Do not broaden current workspace
Roots to make external Git metadata appear. Implement no such grants in H4-A.
