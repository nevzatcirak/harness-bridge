# H3 late-completion reconciliation — code and test evidence

Date: 2026-10-25 UTC. **Synthetic implementation verification PASS; new live
OpenCode/Claude verification NOT RUN.** No installed binary/service, token,
firewall/timer, Traefik/Swarm, DNS or native history was changed. No real prompt
or native interruption was sent. Current private service remains read-only.

Baseline application PR #3: e5160d518dda9c78e4394a77afb7818d0ab1484d.
Incident evidence: deployment PR #1
[8e52c04](https://github.com/nevzatcirak/ix-deployment/blob/8e52c04e6ec1efc92373cc50e0de8082729f4f9c/harness-bridge/SINGLE_WRITER_MIGRATION_ACTIVATION.md).
Decision: [ADR-011](adr/ADR-011-late-completion.md), preserving ADR-010.

## Root cause and exact timeout boundaries

The real incident's second native run completed in305.952s, with correct history,
parent/context/cwd proved later. The client stopped at 150s and the configured
background monitor was 300s. The native latency cause was not established.
Increasing only a timeout does not fix the frozen-read/restart boundary.

| Path/boundary | Previous behavior | New behavior |
| --- | --- | --- |
| MCP send_prompt | ACL/project validation, reserve session/key, return Bridge-generated execution/user-message IDs; background Prompt may not yet be acknowledged | Same, plus freeze original raw/canonical workspace metadata |
| Native async POST | OpenCode204 acknowledges acceptance, not successful execution; native provides no Bridge execution ID | Unchanged; exactly one Prompt attempt, even on lost reply |
| Request timeout/cancellation | Before admission cancels preflight; after admission native background work is detached | Unchanged; read cancellation cancels only its evidence attempt |
| Client polling budget | Client can stop polling independently of execution/monitor | Keep writer/ledger intact under newly approved late-evidence policy; no new key |
| Monitor deadline | Worker exits unknown, keeps busy guard, but Get/Result only return stale snapshots forever | Same bounded worker exit; structured reason/timestamps, bounded reads can now reconcile |
| Native duration | OpenCode may continue beyond request/client/monitor lifetime | Unchanged; no implicit Abort or permission response |
| Native reads | Status/history/permissions during background lifetime only | Metadata/status/history/permissions additionally queried by authorized status/result after worker exit |
| Terminal proof | Parent/session/final-time/finish-or-error plus idle/absent status | Strengthened with original user present, workspace pin, positive completion time, named native error, no unfinished correlated assistant and successful/no-pending permission check |
| Late transition | No promotion after worker exit | SAME execution -> completed/failed/interrupted with native ended_at and reconciled_at; release own busy reservation exactly once |
| Shutdown/restart | Native may continue; in-memory records lost on restart | Close cancels/joins monitors/probes and marks unresolved; restart limitation remains explicit, not durable recovery |

HTTP204, native idle/absent status and elapsed time are never task-success proof.
A terminal native failure is different from a missing/failed observation. Native
completed state still does not establish the user's acceptance criteria.

## Code paths and reconciliation contract

- internal/execution/manager.go: structured unresolved view, monitor exit metadata,
  acceptance workspace pin, local-only Snapshot, read-triggered Get/Result and
  bounded/serialized explicit interruption; shutdown joins managed work.
- internal/execution/reconcile.go: single in-flight read per execution, cooldown,
  global native-probe slots, context-aware operation gate, workspace validation,
  terminal evidence and immutable snapshot/guard commit.
- internal/adapters/opencode/client.go: Directory accessor only; existing supported
  Get/Status/History/Permissions/Prompt/Abort endpoints unchanged. Runner now
  requires metadata and permissions, rather than permitting proof without them.
- internal/mcp/server.go: Snapshot -> current principal/session/root authorization
  -> original workspace validation -> Get/Result reconciliation. No upstream
  reconciliation before ACL checks, including cached terminal reads.

After background exit, one status/result call can do ONE bounded evidence probe.
It never invokes Prompt, allocates a new execution or changes a key/fingerprint.
Concurrent calls join that probe, then see a consistent snapshot. Terminal results
are immutable; result messages replace the prior snapshot, not append duplicate
records. Returned timestamps/message buffers are copied to avoid caller aliasing.

Evidence must match exact native session, original user ID and assistant parentID.
Current metadata raw/canonical workspace must equal acceptance metadata. If an
assistant supplies cwd, its canonical directory must match too. A positive native
completion timestamp and final stop/end_turn or named structured error are needed;
malformed errors/nonterminal tool finishes cannot prove a terminal outcome.
Pending/unavailable permissions, busy/retry status, missing user, wrong parent,
unfinished assistant or identity mismatch cannot release admission. Missing status
is allowed ONLY with full correlated terminal proof and no pending permission.

On inconclusive late reads: unknown + original unresolved cause/since, updated
last_checked_at and retained guard. Pending native permission is explicitly
waiting_for_permission. Security mismatch returns FORBIDDEN, not another project's
messages. Once evidence proves terminal, promote the existing record atomically,
clear unresolved, use native completed time as ended_at and Bridge observation
time as updated_at/reconciled_at. No completion is inferred after restart.

Existing seven tool input/transport/auth contracts remain unchanged. Optional
Execution fields and ended_at semantics are specified in [CONTRACTS.md](CONTRACTS.md).
Output schemas are generated from the updated Go DTOs. Strict old clients must
accept/refresh optional response fields; normal Go DTO decoders ignore additions.
Idempotent replay remains local/cached and does not itself trigger reconciliation;
use status/result reads. Changed payload still returns IDEMPOTENCY_CONFLICT.

## Resource and cancellation bounds

- Background default remains 300s and 500ms polling, one worker per admitted run.
- Each on-demand attempt has a 5s bound INCLUDING queue/gate waits, or a shorter
  caller deadline. Existing adapter requests honor context and have10s transport
  limits; MCP's prior authorization lookup retains its own10s bound.
- Maximum4 concurrent on-demand probes per manager, including explicit interrupt
  evidence checks. Per-run operation gate serializes monitor/probe/Abort. Reads
  coalesce; completed attempts have the poll interval cooldown (default 500ms).
- No permanent late-poll goroutine, daemon or retry loop. Without another read
  after expiry, no additional native polling occurs. Cancellation never sends
  native Abort. Explicit interrupt checks fresh evidence and refuses proven
  terminal work or unavailable native identity/state; acknowledgement alone
  never releases the guard.
- Close cancels manager-bound reads, waits for active managed monitors/probes,
  then marks unresolved records MANAGER_CLOSED. Terminal snapshots stay terminal.
  The adapter/Runner must honor supplied contexts; no mechanism can forcibly
  interrupt arbitrary noncooperative Go code.
- IDs/keys/results retain the existing process-lifetime in-memory retention.
  There is no newly added TTL that silently permits duplicate submission, but
  long-lived memory/history retention remains a known limit. No DB/Redis/broker
  or autonomous supervisor was introduced.

## Deterministic tests

The full300s deadline and 306s late completion run under standard-library
`testing/synctest` virtual time (Go minimum 1.25 already required by this repo).
They do not wait five wall-clock minutes. Bubbles require their goroutines to
exit; Close/cancellation tests explicitly drain blocked reads. Official SDK
integration uses real loopback TCP/TLS and an OpenCode-shaped HTTP fixture with a
short controlled monitor deadline; it does not call the installed OpenCode.

| Requirement | Test evidence |
| --- | --- |
| Completion after old 300s deadline | TestLateCompletionAfterRealMonitorDeadline: expired 300s, native proof 306s, identical IDs, native ended_at, reconciled_at, retained then released guard |
| Still-running after expiry | Same test: busy native remains unknown with last_checked_at; new key blocked |
| Failure/interruption found late | TestLateReconciliationTerminalErrors |
| Pending permission, even with final message | TestLatePermissionBlocksEvenWithTerminalMessage |
| Unavailable permission API | TestUnavailablePermissionsCannotProveTerminal |
| Concurrent status/result polling | TestConcurrentLateStatusResultAndInterrupt:32 callers share one native history probe, immutable result and no duplicate Prompt |
| Replay/conflict and blocked new key | Late deadline test plus prior concurrency/idempotency regressions |
| Cancelled client, native continues | TestCanceledReconciliationCallerDoesNotAbortNative; existing admission-cancellation tests; no Abort |
| Correct/incorrect parent/session/user/time/finish | TestLateWrongCorrelationCannotReleaseGuard; successful late case |
| Workspace/session mismatch | TestLateIdentityAndWorkspaceMismatch; TestSDKReconciliationAuthorizationAndOriginalWorkspace (even a move within an allowed root denied before probing) |
| Interrupt versus late terminal proof | TestExplicitInterruptAndLateProbeShareOperationGate and concurrent interrupt case; zero Abort after proven completion |
| Native state unavailable at interrupt | TestInterruptRefusesUnavailableNativeEvidence |
| Shutdown/restart | TestCloseCancelsBlockedReconciliation; existing restart mapping/idempotency limitation test |
| No infinite workers/global bound | TestLateReadsBoundedCanceledAndNoBackgroundPolling (1000 virtual seconds without reads); TestReconciliationGlobalConcurrencyBound (four active, cancel/drain all) |
| Submission reply lost | TestUnconfirmedSubmissionCanReconcileWithoutResubmit; updated TestSDKLostNativeReplyNeverResubmits: one POST, late proof promotes same run |
| Official SDK late status AND result | TestSDKLateCompletionReconciliation, verified TLS, unchanged IDs/ACL and one fixture POST |
| Existing read-only/authorization behavior | Prior TestSDKReadOnlyStaging and execution ACL/HTTP/TLS suites retained |

During development the old lost-reply SDK test expected unknown forever while its
fixture already supplied final proof. It correctly failed under the new read
semantics. The fixture now holds native work active to verify unknown/no-retry,
then releases terminal evidence and asserts reconciliation. No production guard
was weakened to preserve the obsolete expectation.

## Exact final quality results

All commands below exited 0 after the final source changes:

```sh
gofmt -w cmd internal
go build ./...
go vet ./...
go test ./... -count=1
go test -race ./... -count=1
git diff --check
```

Unit results: cmd/bridge 0.029s; adapters 0.161s; execution 0.117s; mcp 1.647s;
security 0.010s. Race results respectively 1.047s/1.315s/2.341s/4.409s/1.029s,
no race reports. Smoke/privatecheck command packages and mcpclient report no test
files. All existing H1/H2/H3 tests ran; targeted development suites also passed.
These are synthetic/provider-fixture results, not a new real 306s OpenCode result.

Read-only host inspection before/after: private service PID 4036709 / NRestarts=0,
test OpenCode PID 3679520 / NRestarts=0, both active. Installed private binary SHA-256
remained 603e04f76e847344b9898f9958562668a4edc08a063ca 568479abb95b3e8e31e.
No running binary was installed/replaced, no credential was read/rotated for live
calls, no live native API/prompt was exercised, and no unit/firewall/routing change
was made by this task. Deployment PR #1 receives a documentation handoff only.

## Restart/operator boundary and proposed next verification

This cannot recover the old migration writer's discarded execution IDs. A new
manager returns NOT_FOUND, not completed. Current startup does not reconstruct
or silently classify prior executions. Native busy/unfinished-assistant/pending-
permission checks still gate new admission, but absent status alone cannot rule
out a previously uncertain submission. Operator must fence clients and compare
all private accepted user IDs against native final parent chains/status/permissions
and original workspace BEFORE restarting a writer. If proof is absent, stay
read-only. Never retry an old key in a new process or erase history to proceed.

Next live run needs fresh explicit migration/prompt approval. Keep current private
read-only service untouched until then. Build/review this candidate, quiesce and
reconcile current native history, use existing lock inodes and the same original
session/root, and start exactly one updated writer under the reviewed deployment
procedure. Approve an overall late-observation window (proposed 10min) with 2–5s
read polling. At 300s, unknown stops NEW submissions, not that writer process or
its evidence collection. Preserve IDs; fetch terminal status AND result through
that same process when native proof arrives. Verify blocked new keys until proof,
unchanged native identity/cwd/history, context continuity and zero duplicates.

Only a real completion AFTER monitor expiry proves real late reconciliation. If
the actual provider finishes earlier, record normal success without claiming the
late branch; do not manufacture delay through shell/tool work or exceed the
approved prompt count. On overall-budget exhaustion, report unresolved and use
only the specifically approved stop/ledger/recovery path. No auto Abort, permission
approval, new-key retry or writer restart. Public ingress and actual Claude Chat
remain separate gates; restart-safe exactly-once remains unsupported.
