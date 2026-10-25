# ADR-014 — Workspace evidence identity and bounded admission

Date: 2026-10-25. Status: implemented for review; not deployed. Supplements
ADR-010/011 without changing writer ownership or the seven original tool schemas.

## Problem and decision

Canonical path equality does not detect a workspace directory replaced at the
same name. H4-A's policy pins approved root metadata, but a workspace can be below
that root. Execution evidence must remain bound to its accepted directory, even
when the directory text and native session ID are unchanged. Regression tests
reproduce accepted replacement/cached-result/observation cases on H4-A 0b3c319.

Capture the accepted canonical workspace's directory FileInfo. Require clean,
absolute, non-alias directory spelling and compare device/inode via os.SameFile
when validating execution evidence. Recheck immediately before Prompt, after
native observation reads, before explicit Abort, and when serving same-key or
cached execution results. Existing MCP authorization still runs first. Snapshot
is deliberately local evidence for authorization; it performs no native reads
and is not exposed as an unauthenticated result path. A replacement denial does
not rewrite an immutable terminal record or release an uncertain busy guard.

A post-admission pre-submission metadata/identity failure submits no Prompt and
retains unknown/PRE_SUBMISSION_UNAVAILABLE with the same execution/key guard.
It is not permission for automatic retry or a new key. Reconcile through the
existing evidence workflow; do not adopt replacement storage. Terminal results
still require ADR-011's native parent/session/completion/permission proof.

Admission reservations are also managed work. Register them under the manager's
closed gate; Close cancels their native contexts and joins cleanup before
returning. Preflight and queueing together have a 5s deadline with four concurrent
native admissions, separate from the four read-reconciliation slots. Rejected or
canceled admissions clear their reservations/fingerprints and wake same-key
waiters. Accepted monitoring retains its 300s independent lifetime. Context-aware
Runner implementations remain required; a stuck filesystem syscall is not made
interruptible by these changes. No native Abort is implied by Close/cancellation.

## Security limits and phase boundary

These checks detect observed replacements, not a race-free lease covering native
HTTP operations. No descriptor is passed to OpenCode; stat metadata is not a
permanent inode pin, so inode reuse/mount manipulation and swaps between checks
remain residuals. No rootfs/native tool isolation is supplied by an MCP ACL.

The current H4-B continuation prioritizes this security gate before the earlier
H4-B provisioning plan. Session creation, repository-wide grants, launch and
shutdown remain design-only. No new DB, broker, supervisor, adapter or tool is
introduced. The synthetic Linux confinement fixture is test code, not a service
or a remotely callable launcher. Same-UID credentials, imported hard links,
interpreted writable scripts, egress and exact native-runtime compatibility remain
blocking acceptance gates; see H4_B_SECURITY_EVIDENCE.md. H4-B full acceptance and
production H4-A promotion are not declared complete.
