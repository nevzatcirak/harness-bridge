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
metadata until an operator separately approves the necessary roots.

## Bounds and security

`find_sessions`: ID-sorted, exact repository/workspace/parent filters, limit default
50, maximum 200; offset 0–1000; optional next_offset. ACL maximum 1000 for new
registry tools; total discovery deadline 10s. Repository lists deduplicate these
bounded authorized results. Offset pagination is a fresh view, not a snapshot:
concurrent native metadata changes may reorder membership. Errors do not silently
return partial discovery. Old list_sessions retains its original no-input shape.

Git reads use fixed `/usr/bin/git`, argument arrays, no shell, a sanitized
environment, no optional locks or network commands; 2s total per inspection and
8 KiB per output stream. Only rev-parse and symbolic-ref execute. No status scan,
fetch, checkout, hooks or repository executable. Branch and HEAD can be absent
for unborn/detached repositories. Rejected absolute-path aliases include symlink
components and unclean traversal paths. Native directory claims never authorize
access. Every old tool uses the same stricter canonical path check.

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
