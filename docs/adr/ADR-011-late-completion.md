# ADR-011 — Bounded read-side reconciliation after monitor expiry

Date: 2026-10-25. Status: accepted in code; deployment NOT performed.
Maintains ADR-010's one host-local writer, permissions and process-local records.

## Incident and boundary

The approved migration observed a native turn complete after 305.952s. Its client
stopped after150s, and Bridge's bounded background monitor was 300s. Native work
continued independently; later history proved the same session, original parent,
context marker and workspace. Stopping Bridge destroyed the in-memory execution
mapping, so result retrieval through that writer was never established.

The missing capability was not a larger timeout: after work() exited, Get/Result
returned only a frozen snapshot. No later read could prove completion and release
the same-session busy guard. Keep the existing 300s monitor unchanged and make
known executions reconcilable through authorized reads in the SAME process.

## Decision

- Monitor expiry/submission uncertainty is nonterminal. Preserve original
  execution/native user message IDs, key/fingerprint and session guard. Expose
  state unknown, structured unresolved cause/timestamps and monitor_ended_at.
- After background observation stops, get_execution_status/result performs at
  most one fresh native evidence attempt. No new execution ID, Prompt or native
  permission response is generated. Replays retain the original execution.
- Bound each attempt to 5s including queueing; coalesce concurrent pollers per
  execution, cap on-demand native probes at 4 per manager, and reuse the cached
  outcome for the poll interval (default 500ms). Explicit interrupt evidence uses
  the same operation gate/global slots and a bounded context.
- Authorize FIRST: MCP gets a local-only Snapshot, revalidates current native
  session/root policy, and checks the original workspace identity before any
  reconciliation. Pin raw and canonical directory at acceptance. Native reads
  use the original directory-scoped adapter. Wrong session, moved/retargeted
  workspace or differing assistant cwd cannot prove a terminal result.
- Require the exact native user ID to be present, assistant parentID and native
  session to match, positive native completed timestamp, final stop/end_turn or
  named structured native error, no unfinished correlated assistant, idle/absent
  native status, and a successful permissions read with no matching pending
  permission. Idle,204 or elapsed time alone never suffice.
- Commit terminal state, native ended_at, observation/reconciled timestamps,
  result snapshot and guard release atomically once under the run mutex. Replace
  result snapshots; never append another terminal record per poll. Terminal
  state is immutable. An on-demand read can resolve completed/failed/interrupted.
- Inconclusive evidence retains unknown and the guard. A native permission wait
  remains explicit waiting_for_permission, including after expiry. No native
  permission approval or automatic Abort is added.
- Serialize monitor, read probe and explicit interrupt using a context-aware
  per-run operation gate. Interrupt rechecks native evidence: if completion is
  already proven it returns INVALID_INPUT without Abort. If native identity/state
  cannot be read it refuses interruption, retaining uncertainty. The native API
  offers no atomic compare-and-abort; ADR-010's exclusion of other writers remains
  essential. Do not claim that arbitrary external native clients are fenced.
- Caller cancellation stops its read attempt, not accepted native execution.
  Close cancels/joins managed monitors/probes and marks unresolved records
  MANAGER_CLOSED. No worker polls indefinitely after the monitor ends.

## Compatibility and limits

Seven tools, inputs, transport, Bearer, ACLs and ownership locks stay unchanged.
Execution outputs gain optional unresolved, monitor_ended_at and reconciled_at.
ended_at now records the native terminal timestamp; updated_at records Bridge
observation time. Strict old response parsers must accept new optional fields.
Runner now requires native metadata and permission reads; the only production
adapter already provides them. No dependency/configuration/infrastructure added.

Records and keys still reside only in memory and remain until manager/process
lifetime ends; no new TTL eviction can silently forget a key. Long-lived history
retention/memory limits remain an existing gap. A new process does NOT recover an
old execution ID or infer completion from NOT_FOUND. Before restart fence writes,
retain the private ledger, correlate every accepted user message to native final
history, verify original session/workspace/status/permissions and reconcile all
uncertainty. Never resubmit an old key in a fresh lifetime. Remain read-only if
proof is missing. Host flock protects cooperating writers, not native work after
Bridge exit. No durable exactly-once claim, DB, broker or supervisor is introduced.

For the next live acceptance, obtain approval for a bounded late-evidence window
in the SAME writer process. A300s monitor expiry stops new submissions, not
read-side evidence gathering. Use a separately bounded overall client window
(e.g. 10min) and 2–5s polling; per-request attempts stay short. If the overall window
expires, report unresolved and take the specifically approved recovery path—do
not automatically restart, replay, approve permissions or interrupt native work.
