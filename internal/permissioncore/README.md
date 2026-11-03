# Isolated request-bound permission core

P2-PERM-CORE-01 implements local RAM bookkeeping, not an approval channel or a
native permission relay. Direct Claude Chat human approval remains
**NOT_VERIFIED and disabled**. The public `New` constructor always leaves native
dispatch disabled; there is no enable flag, public provider injection, socket,
CLI, MCP approval tool or production OpenCode reply method in this package.
Only package-internal tests install a fake native provider.

## Baseline and ownership

This task lives in `/tmp/hb-p2-perm-core-01`, branch `feat/p2-perm-core-01`, based
on committed `db9da6b0cb3721ad6dc2a271726c01632c98381c` (76 tracked files).
It includes the existing session/execution manager, ADR-010/011/014, session ACL
and project-root policy, named remote-principal boundary, and OpenCode adapter.
The adapter's `Permission` contains only `sessionID`; `Permissions` only reads
`GET /permission`. It cannot currently supply this core's exact request/tool-call
correlation and has no permission reply method.

Uncommitted integration files were **not imported**. This worktree has no
Audit Journal, Mutation Gate, ADR-016/lifecycle/control schema or Roadmap file.
Roadmap v1.3 was read from the active integration worktree, SHA256
`4999c0932387617c35939bbe013e9e9b7ba9c115c07cb1d85641c4aa8883809c`.
The source specification and existing committed contracts were read locally.
The feasibility baseline is `/tmp/hb-p4-perm-feas-01/FEASIBILITY.md`, including
its validation and commit-pinned OpenCode1.19.4 source snapshots. No active
integration source, tests or documents were written.

One Core must be owned by the existing server-side execution/authorization owner.
`Authority` delegates to that owner's authenticated identity and current policy;
it is not a second identity provider or authorization service. The core cannot
prove native lineage, canonical root/inode, execution/tool parentage or transport
authentication from strings. The future trusted producer/authority must supply
and revalidate those facts. No cross-process exclusivity is added here; reuse the
existing ADR-010 ownership boundary when integrating.

## Request, decision and consumption contract

An observation binds configured native authority, independently verified native
process lineage and version, exact request ID/generation, native session, existing
Bridge execution, original user message, tool message/call, verified root/workspace,
current policy revision, authenticated principal, tool/action and scope digest.
All IDs are bounded opaque server/native identities, not client-provided authority.
Missing correlation fails closed. Version support is deliberately restricted to
the reviewed `opencode-1.19.4` semantics. The minimal supported tool/action pairs
are read/read, edit/write, write/write and bash/execute; other tools fail closed.
This internal domain contract is new implementation scope, not a native JSON or
Audit Journal wire-schema extension.

Scope material is exact UTF-8 producer bytes (1..16384 bytes), SHA256 hashed under
the internal `HB-PERM-SCOPE-v1` domain and discarded. Bindings also compare the
typed tool/action/resource count. The future observation adapter must define a
complete deterministic representation of native permission, patterns, metadata,
always patterns and tool correlation. This core does not parse native JSON or
claim semantic equivalence across alternate producer encodings. Caller-supplied
fingerprint overrides are rejected. No raw prompt, credential, command, path or
sensitive native output is retained in core state or View. Opaque ID generation
and secret-safe classification remain trusted-producer responsibilities.

Presentation exposes only fixed metadata, coarse tool/action/resource count and
the digest, with a fixed warning that details are withheld. It is **insufficient
for human consent by itself**. `Authority.VerifyHuman` must independently resolve
a server-held receipt for the exact Challenge/Choice and verify separately
authenticated human identity, explicit confirmation, complete safe presentation,
principal/scope mapping and revocation. This package supplies no implementation
of that channel. Model text, an `approved=true` claim, ordinary MCP tool consent,
same UID or a Unix socket alone cannot satisfy it. HumanEvidence is trusted
authority output, never an externally accepted DTO or proof synthesized here.

Each challenge includes a fresh process-local epoch, unique local ID, full binding
and immutable monotonic elapsed deadline. Default TTL is 120 seconds, configurable
only downward (minimum1ms); queueing/revalidation counts against it. This is a
Bridge consent bound, not a native expiration guarantee or ADR-016 Prompt grant
TTL. `time.Since` preserves monotonic behavior; tests replace only a private clock.
Native expiration/disappearance must be represented as unavailable/unknown unless
exact positive resolution is available.

Transitions:

| Transition | Core effect | Native reply allowance |
| --- | --- | --- |
| Observe -> Pending | Authorized exact request, fresh challenge | 0 |
| Pending -> Decided | Exact verified explicit allow-once receipt | 0 |
| Pending -> Denied | Exact verified denial; local intent only | 0 |
| Decided -> Reserved | One sealed owner Claim under mutex | 0 |
| Consume | Spend Claim before external verification/read; never refund | Public constructor:0 |
| Fixture Reserved -> Attempted | Final identity/policy/human/TTL/close checks; set one attempt marker | Fake-only:at most1 once |
| Attempted -> ReplyObserved | Fake reply ACK; not native resolution or task success | No second attempt |
| Attempted -> Uncertain | Lost reply, cancellation/Close or abandoned fixture call | No retry/refund |
| Pre-attempt -> Expired/Canceled/Stale | Authority removed, no native cancellation implied | 0 |
| Pre-attempt -> ExternallyResolved | Exact positive native resolution evidence | 0 |
| Missing subject -> Uncertain | No fabricated native identity or actor | 0 |

Same decision/receipt returns recorded disposition without refreshing TTL or
creating a new claim. Conflicts and reuse of a receipt on another request fail.
The table retains spent/stale/expired identities: default256/max4096 entries,
no tombstone eviction or reset-to-pending. Reobservation never renews consent;
changed scope/generation under the same native authority/lineage/request ID makes
that entry stale. Each fresh request needs fresh positive correlation and consent.

A retained entry is owned by the principal in its immutable binding. `Observe`
by a different principal on the same native authority/lineage/request ID returns
`ErrAuthority`, the same error as an unauthorized caller, without the entry's
binding or state, and leaves the entry unchanged. Only its owner can make it
stale, and only while the Authority still authorizes the owner for the entry's
original binding (checked outside the mutex, then revalidated). Residual limit:
native request IDs are a shared namespace, so a colliding principal still cannot
create its own entry under that key and could infer that the key is taken.

`Consume` rechecks current policy and human receipt before and after native
freshness reads in the fixture path. It never holds its mutex across callback IO.
This mutex/attempt marker is **not** the approved Mutation Gate M and is not a
durable witness. No enabling integration is supplied. Native list/check and reply
cannot be made atomic by this core; the fake includes a TUI-after-read race to
demonstrate one attempted call with uncertain attribution, rather than pretending
the upstream API offers a fingerprint CAS. Fake replies use ID-only native
semantics, so a missing core binding check cannot be masked by a stronger fake.
Public dispatch remains closed.

Cancellation/Close never imply native Abort, native rejection or guard release.
Close invalidates unattempted rights; an earlier fake attempt remains uncertain
and single-use. Context-aware callbacks are required; uninterruptible IO is not
forcibly stopped and a bounded shutdown/join is not promised by this isolated core.
Reconciliation never reconstructs execution/key/result state or revives rights.
No receipt/claim import or persistent store exists. New Core means new epoch and
empty RAM; old claims/decisions fail. Unknown old mappings cannot be observed as
known executions. Fresh producer correlation is a prerequisite, not replay.

## Native rejection blast radius

Pinned OpenCode source (commit
`148842ef7cb1bc34192b3fff57d0f9721ab9c801`,
`packages/opencode/src/permission/index.ts`, lines111–127) rejects other pending
requests in the same native session after `reply=reject`. A singleton list cannot
exclude a new pending request racing that list. This core therefore **never
dispatches native reject**, including in its fake provider interface. Explicit
Deny preserves local denial and records `same_native_session` reject scope;
native pending/resolution is a separate last-observed fact. Other requests and
sessions are not auto-approved or rejected. Native operator intervention remains
necessary. The tests separately calibrate the fake's session-wide reject behavior;
that calibration is not a production native mutation or an exact-reject feature.

## Integration prerequisites and unsupported guarantees

- Read-only native DTO/adapter and positive tool/message/parent-to-execution
  correlation, full deterministic native scope material and verified lineage.
- A separately authenticated explicit human channel, complete secret-safe scope
  presentation and receipt verifier bound to the same server-side policy authority.
- Separately reviewed permission-specific durable witness and Mutation Gate
  integration. Existing21-event control codec does not automatically encode
  permission decisions. Reuse the single writer/coordinator and B/Q/F/M semantics;
  a Prompt/recovery grant never authorizes a permission response.
- Native freshness/race semantics, ownership, policy and safe delivery acceptance
  before any future dispatch enablement. No unsafe request-scoped reject promise.

No direct Chat approval, real human authentication, real native permission reply,
durable restart recovery, exactly-once, native check/reply atomicity, execution
completion, current production behavior or Phase5 E2E acceptance is proved here.
Tests are deterministic fake-authority/fake-native core evidence only.
