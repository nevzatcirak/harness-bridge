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

A post-admission pre-submission metadata/identity failure calls no native Prompt.
P2-REC-02B records an immutable local failed outcome, retains the accepted key and
IDs, and releases only that run's busy reservation atomically with the local
outcome. Same-key replay returns the original failure subject to the existing
workspace/ACL checks; a different key must pass fresh admission. No native Abort,
message or completion timestamp is generated. Possibly-submitted outcomes still
require ADR-011 native parent/session/completion/permission proof.

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

The isolated P2-REC-02 proposal (PR #6) contained only execution workspace
identity and admission coordination. MIG-A-02 (below) additionally carries the
non-overlapping H4-B material from fdd5118; neither adds a credential authority,
provider/plugin integration or new MCP tool. H4-B provisioning and production
promotion remain outside this change. ADR-015 remains PROPOSED and its review
pending; no restricted experiment is repeated here.

The synthetic Linux confinement fixture is test code, not a service or a remotely
callable launcher. Same-UID credentials, imported hard links, interpreted writable
scripts, egress and exact native-runtime compatibility remain blocking acceptance
gates; see H4_B_SECURITY_EVIDENCE.md. H4-B full acceptance and production H4-A
promotion are not declared complete.

## Isolated review clarification (2026-10-29)

The original P2-REC-02 patch copied the execution implementation from
fdd51181b7a7bb441ee6e3557bbcc8542a1a9612 onto H4-A
0b3c319c96b0da0f921d63e8be6e5b063c66b0fc. P2-REC-02A rejected the
accepted-but-never-submitted lockout and Abort paths. P2-REC-02B revises only those
lifecycle paths and their tests; the admission/workspace hardening remains.
No unrelated H4-B experiments are imported.

PRE_SUBMISSION_UNAVAILABLE is now local_failure.code, not an unresolved reason
or a new execution state/top-level MCP error. The public record has state=failed,
local_failure={code,recorded_at}, no unresolved/ended_at/reconciled_at and no native
result messages. recorded_at is a Bridge timestamp. message_id remains the
allocated correlation ID; it does not assert that a native message exists.

An internal mutex-protected submissionAttempted marker is set once immediately
before Runner.Prompt invocation. After that boundary Prompt is invoked exactly
once, even if cancellation races with invocation; a true marker never becomes
false because of an error/reason string, timeout, missing history or disconnect.
Only the unattempted branch can commit a local failure. Terminal outcome and
reservation release commit under run.mu with the conditional busy-ID removal;
the accepted key/fingerprint are not deleted. Cached read, late probe, interrupt,
monitor cleanup and Close cannot revive or rewrite the local terminal outcome.
Snapshot clones local_failure so callers cannot mutate manager evidence.

A known never-submitted execution cannot invoke Abort. A fresh different-key
request may run after release if current policy, original native session metadata
and current workspace preflight permit it; no replacement storage is adopted by
the old execution. Changed payload under the old key still conflicts. Possibly-
submitted unknown retains its guard and IDs and is never automatically retried.
A native terminal failed outcome has no local_failure and retains native ended_at;
local failure invents no native completion. Process restart still loses both kinds
of record and key mapping; local finalization is not durable restart recovery.

The four-slot/five-second budget applies to new reserved manager admissions.
Same-key followers wait on the original pending admission or their caller context;
MCP authorization before manager admission still has the adapter's separate bound.
The accepted background worker is detached from the five-second admission context
and revalidates immediately before Prompt within its existing monitor context.
Stat/path syscalls and context-ignoring Runner implementations are not made bounded
by this change. Close joins managed work but cannot guarantee prompt return from
uninterruptible operations. Native execution may continue after Bridge exits.

## MIG-A-02 reconciliation (2026-11-03)

Reconciles PR #5 head fdd51181b7a7bb441ee6e3557bbcc8542a1a9612 into PR #6 head
db9da6b0cb3721ad6dc2a271726c01632c98381c over common base 0b3c319. PR #6's
execution code (manager.go, reconcile.go, hardening_test.go) is a superset of
fdd5118's identity/admission hunks and is kept verbatim. The only conflicting
semantic is the accepted-but-never-submitted path: fdd5118's
unknown/unresolved PRE_SUBMISSION_UNAVAILABLE with a retained guard is superseded
by P2-REC-02B's local failed outcome above, and fdd5118's CONTRACTS section is
dropped in favour of PR #6's. Non-overlapping fdd5118 material (tests/confinement,
H4-B evidence documents, README/ARCHITECTURE/provisioning-plan notes) is carried
unchanged except for a supersession note in H4_B_SECURITY_EVIDENCE.md, plus new
sibling provenance notes (H4_B_EVIDENCE_PROVENANCE.md and
tests/confinement/evidence/PROVENANCE.md) that leave the evidence bytes intact.
That evidence remains an observation of fdd5118 and is not re-asserted for this
head.
