# ADR-010 — One host-local MCP writer, read-only secondary ingress

Date: 2026-10-24. Status: accepted for MVP; deployment migration NOT authorized.
Supersedes no native permission or H1/H2 lifecycle contract.

## Audit and decision

Choose **A**: one Bridge process owns all MCP mutations for a native session,
including remote access. Local clients that need mutation use that same process
and authenticated HTTPS endpoint. Additional Bridge instances remain read-only.
Keep one execution manager for this writer. Option B (remote ingress forwarding
mutations to a local writer) adds a privileged RPC boundary, authentication,
correlation and uncertainty handling without a demonstrated requirement. No
forwarding endpoint, Redis, DB, broker, distributed lock or supervisor is needed.

Audited paths:

| Path | Guarantee and scope |
| --- | --- |
| `internal/mcp/server.go` `call` | Read-only guard before native calls; per-session ACL and canonical project authorization; directory-scoped native client; explicit interrupt tool reauthorizes current metadata |
| `internal/execution/manager.go` `SendWithRunner` | Process-local reservation before native I/O; native status/history/permissions preflight; same-session exclusion |
| Manager keys/fingerprints | Process-local session + key + SHA-256(prompt); same key/payload replays same execution/message IDs; changed payload is typed IDEMPOTENCY_CONFLICT |
| Pending preflight channel | Concurrent identical keys wait for admission with caller cancellation; different keys receive SESSION_BUSY; unrelated sessions need not wait |
| `work` | Caller disconnect after admission does not cancel native work; detached bounded lifetime; one native Prompt call, never automatic retry |
| `observe` | Match native session and user message ID / assistant parentID; terminal completed timestamp + final finish/error proof, not idle/204 alone |
| Native permission state | Pending native permissions block new submission; monitor explicitly exposes waiting_for_permission; Bridge never grants permissions |
| Interrupt | Explicit authenticated/authorized tool only; submitting refuses interruption, repeated acknowledged abort is suppressed; keep busy until correlated terminal proof |
| Close/timeout/submission error | Unknown outcome retains session guard; no blind retry; transport DELETE/expiry is not native cancellation |
| Linux ownership | Startup advisory flock per native session held until writer shutdown, across cooperating processes on this host |

OpenCode supplies native session/history/directory, status and permission APIs.
The [official server API](https://opencode.ai/docs/server/) documents async prompt,
status and abort, but **does not promise cross-client idempotency or an atomic
Bridge ownership lease**. We rely on no such promise. GET-status followed by POST
is not an atomic operation. Native TUI/direct API writers can race it; the MVP
requires operator exclusivity for prompts in this session while the Bridge owns
it. Native permission approval through the operator's existing workflow remains
allowed and required. Original root OpenCode :4096 is excluded entirely.

## Host-local ownership contract

Every deployed writer must run the updated binary and use the **same existing
protected directory/inodes** via `BRIDGE_WRITER_LOCK_DIR`. Writer startup fails
before opening the listener if the directory is missing, not absolute, insecure,
not owned by the service user, or any session lock is held. Linux only; unsupported
platforms fail closed for writers. Read-only processes acquire no writer locks.
The implementation validates opened directory/file metadata, rejects final
symlinks, hard-linked/nonregular files and nonprivate modes, uses O_CLOEXEC and
LOCK_NB, and releases all partial acquisitions on failure. Session filenames are
hashes of native IDs, independent of URL spelling and workspace changes.

This is advisory host-local ownership, not a distributed lock or a defense
against root, the service UID changing its lock files, disjoint mount namespaces,
other lock directories or old binaries. Protect parent directories, keep a
single shared host mount, and **never unlink/replace lock files while a writer
exists**. Locks protect concurrent cooperating writers, not native API clients.
The CLI owns locks; an embedded server must arrange equivalent ownership.

## Restart, retry and recovery limitation

Execution/key/parent-ID mappings remain memory-only. Shutdown/crash releases OS
locks and loses these mappings; native execution can continue. The next process
cannot recover an old execution ID or tell a reused key from a fresh request.
An explicit regression test demonstrates that a fresh manager can resubmit an
old key against a synthetic idle native runner: this is a documented limitation,
not a durable exactly-once claim. No automated writer restart or credential
rotation is safe while outcomes are unresolved.

Before starting/restarting a writer or rotating its token: fence all MCP writers,
retain recorded execution/message IDs, inspect native status, pending permissions
and correlated history in the exact existing session/workspace, resolve every
uncertain submission, then authorize a new writer lifetime. Old keys must not be
reissued after restart. If reconciliation is impossible, remain read-only. A
request timeout is never permission to use a new key. Within the same live
process, retry the identical key and payload only to retrieve its execution.
An unknown Prompt failure currently stops monitoring; manually reconcile native
history rather than assuming non-delivery or successful recovery.

## Required service migration (plan only)

Current installed local writer and private read-only binary predate this change;
they are **not protected by the new lock mechanism**. Do not enable a second
writer beside the existing local service.

After separate explicit migration approval:

1. Quiesce MCP submissions and confirm exact session, workspace, idle state,
   completed correlated messages and no pending permissions/unknown outcomes.
2. Select the private HTTPS process as the one writer so Claude and any local
   mutation client share its manager. Stop/reconfigure the old local writer as
   read-only in the approved maintenance window. Never restart native OpenCode.
3. Provision `/var/lib/harness-bridge/writer-locks` owned by harness-bridge, mode
   0700 under protected parents; all writers must see the same directory.
   Add the exact path to the chosen unit's writable sandbox allowlist without
   weakening its other hardening, and set BRIDGE_WRITER_LOCK_DIR to that path.
4. Deploy reviewed binary to only the selected writer, retain Restart=no, issue
   a fresh short-lived token, and enable writes only under the separate approval.
   Verify a competing updated writer fails startup, readonly native counters stay
   zero, exact Host/TLS/session/root policy and interruption authorization hold.
5. Rollback stops the new writer first, reconciles any accepted native work, then
   restores the previous reviewed configuration. Never run both writers at once.

None of these service changes is performed by this development task. Public
routing requires its own Approval B. Locking does not grant write/public access.
