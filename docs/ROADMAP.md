# Harness Bridge — Roadmap v1.4

Reconciled baseline: 2026-10-29. v1.3: 2026-11-01. v1.4: 2026-11-03.
Scope: planning and documentation only; no implementation or live acceptance claim.

This is the canonical phase numbering for future planning. H1/H2/H3/H4 are
historical delivery labels, not phase numbers. Existing branches, commits, PRs,
task assignments and evidence keep their original names. The specification's
product requirements remain authoritative; historical evidence records what was
observed at its recorded revision, not what is currently deployed.

## v1.4 scope, provenance and compatibility

v1.4 is the first repository-committed roadmap. It is derived from Roadmap v1.3
(2026-11-01, SHA-256 `4999c0932387617c35939bbe013e9e9b7ba9c115c07cb1d85641c4aa8883809c`,
never committed) and reconciled on 2026-11-03 against the repository, open pull
requests and local evidence packages. v1.3 itself replaced v1.2 (2026-10-29,
SHA-256 `1479796144fba1ca1d7da28cde3ab74cbe25b6bfd266fe70810b75f1b12112c0`).
Host-specific paths were removed; local-only evidence is cited by name and
SHA-256 until it is imported into the repository.

Changes from v1.3:

- Maintainer decision 2026-11-03: the **MVP gate** is the specification's §13
  acceptance (real Claude Chat existing-session continuation with negative
  cases) plus the fail-closed permission fallback. P5-PERM human approval
  round-trip is a required gate of the **Permission Bridge milestone**, not of
  the MVP, and is BLOCKED until P4-PERM evidence changes.
- Maintainer decision 2026-11-03: these phase numbers (0–8) are canonical.
  HARNESS_BRIDGE_SPEC.md §14 is aligned to them.
- Recorded results: P4-PERM-FEAS-01 (2026-11-01) concluded
  PERMISSION_FALLBACK_REQUIRED; P2-REC-06D-A and 06D-A-FIX independent reviews
  APPROVED (2026-11-01). v1.3's "ongoing review" wording is superseded.
- Claude connector Request headers (static_headers, beta) were rechecked on
  2026-11-03 and are available on the maintainer's account.
- Current pull request structure and the dated execution plan were added.

Document status: ADR-010/011 accepted within their recorded scope; ADR-012/013
proposed; ADR-014 implemented for review, not deployed; ADR-015 PROPOSED with
its existing review/access restriction; ADR-016 PROPOSED (uncommitted). Isolated
Audit Journal approval and the accepted single-journal control/admission
direction do not imply production recovery integration. The H4-B creation
capability maps to Phase 7-D; managed process lifecycle H4-C remains Phase 8.

Links to H3/H4 documents resolve once PR #3/#4 are integrated. P4-EVID-01
(`P4_EVID_01_HISTORICAL_ACCEPTANCE.md`, SHA-256
`be88d2530f7378d41a7b4119f4ec302a0c09e76fb9f97706ae1cd046470b1925`) is a local
evidence package pending repository import; its primary sources are pinned in
the private deployment repository at `33270c20a8fce9aa703c3e18f66d1885d108ec7f`.

Invariants for every new work package:

- Existing-session continuation is first priority; Claude Chat is the default
  and only LLM supervisor in the MVP.
- One server-side execution and authorization authority owns all mutations.
  Preserve ADR-010 ownership and ADR-011/014 evidence/admission boundaries.
- Preserve Audit Journal as durable evidence and Mutation Gate as mutation-ordering
  authority. Audit replay is not authorization or execution-state hydration.
  Permission response is a separate mutation; a Prompt/recovery grant does not
  authorize it. Any new event/schema/gate integration needs a separately reviewed
  contract; this roadmap changes none of the isolated implementations.
- No new database, broker or frontend in the current MVP. Never auto-approve,
  bypass native denial, or add another LLM supervisor.
- Preserve the user-accepted **Optional Execution Isolation** direction: an
  explicitly authorized new native session can use an approved existing server
  and workspace without mandatory managed sandboxing. Optional isolation and
  managed lifecycle retain their own safeguards; this does not approve ADR-015
  or weaken the existing H4-C managed-launch security gates.

## Evidence and status rules

- **Completed:** the stated slice has recorded evidence. This does not imply
  merge, deployment, architectural approval or completion of its whole phase.
- **Partially completed:** implementation or preparation exists, with unmet gates.
- **Unverified:** required real-client/runtime evidence is absent; fixtures and
  native endpoint declarations cannot substitute for it.
- **Blocked:** a recorded restriction or unmet prerequisite prevents progression.
- **Future / optional:** planned work, neither implemented nor authorized here.

The v1.2/v1.3 reconciliations used the specification, backlog, architecture
register, ADRs 010–013, H1/H2/H3/H4-A evidence, the H3 acceptance procedure and
the H4 provisioning plan; P4-EVID-01 read commit-pinned deployment evidence.
v1.4 additionally verified branch heads and ancestry on 2026-11-03. None of these
reconciliations performed new live verification.

## Historical delivery mapping

| Delivery / identity retained | Phase mapping | Recorded boundary |
| --- | --- | --- |
| H1, `feat/h1-opencode-session-continuation` | 0, 1; initial 2/3 foundations | Existing-session local two-turn proof, not Claude Chat |
| H2, `feat/h2-official-mcp-go-sdk`, PR #2 | 3; supports 1/2 | Official SDK transport and live local continuation; human review pending in evidence |
| H3, `feat/h3-secure-claude-remote-connector`, draft PR #3 | 4; execution changes belong to 2 and support 5 | Historical operator-reported Claude Chat two turns independently reconciled through native/SDK evidence; full acceptance PARTIAL, current production UNVERIFIED (P4-EVID-01) |
| `7c4f139 feat(h3): enforce host-local single-writer ownership and safe replay` | 2, with 4 integration dependency | ADR-010 ownership/replay implementation and fixture evidence |
| `4cb09fb fix(execution): reconcile late native completion through bounded reads` | 2; prerequisite for 5 | ADR-011 implementation; new live reconciliation not established |
| H4-A, `feat/h4-a-project-aware-sessions`, draft PR #4 stacked on H3 PR #3 | 0/3 discovery and capability contracts; backend foundation for 7 | Existing ACL-scoped discovery, not a developer UI |
| `eb46a60 feat(h4): add repository-aware session discovery and native capability guidance` | 0/3, future 7 foundation | Initial H4-A implementation |
| `0b3c319 fix(h4): confine repository metadata reads and bound shared discovery` | 3, future 7 foundation | Review fixes supersede initial metadata-reader/cost assumptions |
| H4-B / H4-C | 7-D / 8 | Proposed authorized new session creation / optional managed harness lifecycle; historical safeguards retained |
| H4-D | 6 | Claude Code discovery/resume research and future adapter plan |
| ix-deployment `feat/harness-bridge-traefik-plan`, PR #1 | 4 dependency | Separate deployment history; no action authorized by this roadmap |

| H1 / H2 code | 1, 3 | Squash-merged to `main` as `10fc3b9` and `5d3c939`; main tree equals H2 head |
| PR #5 H4-B `fdd5118`, PR #6 MIG-A-01 `db9da6b` | 2; 7-D foundation | Execution admission and workspace identity hardening; both stacked on PR #4 |
| PR #10 MIG-A-02 `efab170` | 2 | Merge-reconciles #5 and #6 (`877b247`); independent review passed on exact head; supersedes #5/#6 for integration |
| PR #9 P2-PERM-CORE-01 `3b2660e` | 2 (P2-PERM) | Isolated request-bound permission bookkeeping; native reply disabled; stacked on #6 |
| PR #7 WF-01 `36f344e` | Workflow | CI, agent instructions and PR template; merged to `main` as `477180e` (2026-11-03); not a product phase |

The H3 acceptance procedure at `e5160d5` remains historical preparation.
No branch, commit message or PR title is renamed by this mapping.

## Phase 0 — Feasibility & Capability Matrix

**Status: partially completed.** Goal: separate supported, observed, declared and
unknown capabilities for each exact installed harness/client version.

Completed slices: H1/H2 record existing OpenCode 1.19.4 API observations and local
continuation; H4-A records scoped history pagination, agent inventory and native
endpoint declarations. See [H1](H1_EVIDENCE.md), [H2](H2_EVIDENCE.md) and
[H4-A](H4_A_EVIDENCE.md).

Missing acceptance: consolidate a versioned matrix covering discovery, history,
same-session follow-up, correlation, monitoring, permissions, interruption,
restart, child lineage and resume versus active-terminal control. Each row needs
source/date/version, evidence type, limitations and unsupported behavior. Native
creation/delegation declarations must remain distinct from successful operations.
Claude Code host capabilities remain unverified. Connector authentication was
rechecked on 2026-11-03 against official documentation: static Request headers
are a beta limited to some organizations and are available on the maintainer's
account; OAuth (DCR/CIMD/pre-registered client) remains the documented
alternative. Refresh external claims only in a separately scoped task.

## Phase 1 — OpenCode Existing-Session Proof

**Status: completed local proof; human acceptance remains pending in evidence.**
H1 and H2 record discovery/history and two prompts into the same existing native
session, retained seed history, correlated parent/message IDs, unchanged directory,
marker recall and duplicate suppression through the local MCP client.

Acceptance: retain exact server/session/workspace and revision evidence; do not
create a replacement session or confuse a new server with an attached TUI.
No additional local proof is required by this documentation change. Real Claude
Chat proof belongs to Phase 4 and is not implied by this completed slice.

## Phase 2 — Execution Control & Recovery

**Status: partially completed; live late-reconciliation acceptance unverified.**
H1/H2 lifecycle foundations plus H3 ADR-010/011 cover bounded asynchronous runs,
correlation, busy/idempotency guards, permission waits, explicit interruption,
uncertain outcomes, cooperating host-local writer exclusion and bounded read-side
late completion in the same manager. Recorded fixture/race checks passed at their
recorded revisions. See [single-writer evidence](H3_SINGLE_WRITER_EVIDENCE.md) and
[late-completion evidence](H3_LATE_COMPLETION_EVIDENCE.md).

Missing acceptance: an independently authorized real same-process execution must
complete after monitor expiry and resolve through status AND result with the
original IDs, exact terminal evidence, no duplicate prompt, and guard release only
after proof. Record disconnect/reconnect and unresolved-budget outcomes explicitly.
Restart loses execution/key records: operator fencing/reconciliation and read-only
fallback remain the supported boundary. Durable recovery/exactly-once across
restart is not delivered or required implicitly; adopting it needs a separate
decision and acceptance criteria. Native TUI/direct writers are not fenced by
Bridge's cooperative locks.

### P2-PERM — permission-state safety (required foundation)

**Partially implemented; not accepted.** P2-PERM-CORE-01 (PR #9) provides
isolated in-memory request-bound bookkeeping with native reply disabled; its
independent review passed on the exact head, with deferred P3 findings. A
CORE-02 observation candidate exists outside the repository. The requirements
below remain the target. Add a bounded native
permission lifecycle alongside the existing execution state, without creating a
replacement execution or resending its prompt:

1. Discover the exact supported native pending request from authoritative native
   reads/events. Bind native authority/server lineage, native permission ID and
   request generation/fingerprint, native session, existing execution/message
   correlation, verified project/workspace identity and policy revision. A
   repository path, session-level pending list or model text alone does not prove
   which execution owns a request; ambiguous ownership fails closed.
2. Set that execution to `waiting_for_permission`; retain its execution ID,
   busy/uncertainty guard and bounded monitoring. Record exact request scope,
   safe display summary, freshness, expiry and required authorization authority.
3. Accept only a verified explicit human allow-once or deny for that exact
   request and authorized principal. Recheck current ACL/project/root/workspace,
   native pending identity, scope, policy, expiry and exclusive decision ownership
   immediately before native reply. No blanket, session-wide, persistent or
   allow-always permission in the initial milestone; unsupported choices deny.
4. Relay through a verified supported harness mechanism to the **same** pending
   native request; record attempted delivery before uncertainty can cause a
   retry. An ACK alone does not prove resolution or execution success. Read back
   authoritative native state and continue monitoring the **same execution**.
   Do not infer completion from disappearance of the permission or session idle.

Permission safety requirements (fixtures first; real-client proof in Phase 5):

| Condition | Required behavior |
| --- | --- |
| Explicit denial / native deny | Relay an authorized deny if still pending; observe native outcome. Native policy denial is authoritative; no override, permission-mode change, replacement prompt/session or retry to evade it |
| Canceled approval / human timeout / expiry | Invalidate approval authority; never treat silence as allow. Use supported native deny only under documented authority, otherwise leave pending and report intervention required |
| Native TUI resolves first | Fresh read rejects stale Bridge decision; preserve observed native resolution and emit no second reply. Atomicity/race guarantees must be verified; unsafe check-then-reply behavior blocks delivery |
| Duplicate / concurrent decisions | One exclusive decision/attempt owner per exact request. Identical duplicates return recorded disposition; conflicts reject. No second native mutation |
| Response lost / disconnect / native reply timeout | Preserve attempted decision and unknown delivery; reconcile exact native evidence. Do not resend allow, refund consent, release guards or claim success without proof |
| Bridge restart / lost execution mapping | Old approvals/expiry tokens do not regain authority; fail closed, fence/reconcile under the existing recovery boundary. Journal evidence alone cannot restore grants or prove delivery; missing IDs do not mean non-delivery |
| Cancellation of MCP call / completed Chat response | Neither grants permission nor cancels native work; explicit authorized interruption remains a separate operation |

Scope-bound, secret-safe audit evidence must connect request observation, safe
presentation reference, trusted human decision reference, principal, identities,
scope/expiry, decision/attempt, native outcome and reconciliation. Omit secrets,
raw sensitive arguments and capability tokens. Audit unavailability or missing
required durable witness blocks permission mutations. This is a future contract
requirement, not a new Audit Journal record type or Mutation Gate bypass.
Permission TTL and monitoring budgets must be separately specified; the proposed
ADR-016 Prompt-grant TTL is not silently reused as native permission consent.

## Phase 3 — MCP Tool Contracts

**Status: core completed in recorded local evidence; H4-A isolated read-only
runtime acceptance recorded, production promotion not established.**

H2 delivers seven typed tools, official SDK transport, bounded inputs, typed domain
errors and separation of request cancellation from native interruption. H4-A adds
four read-only tools, repository/worktree metadata, scoped candidate selection,
opaque history pagination and conditional native delegation guidance. See
[contracts](CONTRACTS.md), [ADR-012](adr/ADR-012-repository-aware-session-registry.md),
[ADR-013](adr/ADR-013-native-subagent-delegation-guidance.md) and
[review corrections](H4_A_REVIEW_FIXES.md).

Recorded acceptance: P4-EVID-01 references the H4-A isolated acceptance, including
comparison with original seven input contracts, eleven-tool discovery, bounded
history, authorization and read-only guards. Candidate was stopped; production
remained H3. Missing acceptance: complete promotion review for any chosen runtime;
supported Git layouts beyond the observed non-Git cwd still need appropriate
evidence. Preserve unknown-state, paging, unchanged ACLs and discovery bounds.
Isolated acceptance does not establish production eleven-tool behavior.
Define a documented compatibility/versioning policy before incompatible changes.

### Additive permission contract proposal

Preserve the original seven tools and their required inputs/semantics:
`list_sessions`, `get_session`, `read_session_history`, `send_prompt`,
`get_execution_status`, `get_execution_result`, `interrupt_execution`.
Preserve the four existing H4-A read-only additions as well. Proposed additive
names are `list_pending_permissions`, `get_pending_permission` and
`respond_to_permission`; names and schemas require review, are not registered
APIs and do not authorize writes today. The response tool must consume a
server-verifiable, request-bound human decision from the Phase 4 trusted channel,
not accept a model-supplied `approved=true` or claimed principal as consent.
Version and test old-client compatibility, redaction, authorization and errors
before delivery. Transport tool consent and native permission approval remain
separate decisions.

## Phase 4 — Secure Claude Connector

**Status: R3a gap closure PASS (2026-11-03); writer promotion of later code remains gated.**
On 2026-11-03 a real Claude Chat conversation through the authenticated connector, on the
accepted H3 writer `4cb09fb`, completed two turns in the existing OpenCode session with marker
recall (marker absent from prompt 2), same-key replay without native duplication, busy-session
rejection, uncertain-execution handling and auth negatives, independently corroborated from native
storage ([R3a evidence](evidence/R3A_EVIDENCE_2026-11-03.md)). ADR-011 live late completion
remains NOT_EXERCISED. The historical record below is retained.

Historical status (before R3a): historical acceptance PARTIAL; current production UNVERIFIED.
P4-EVID-01 supersedes the initial alignment's
claim that ordinary Chat evidence was absent. Commit-pinned infrastructure records
prove historical authenticated public ingress, operator-reported Chat turns and
independently checked native/Bridge correlations on H3 `4cb09fb`. They also record
the later same-binary credential rotation/manager restart. These records do not
prove today's runtime, H4 promotion or full acceptance. Historical application
[H3 evidence](H3_EVIDENCE.md), [connector guide](H3_CONNECTOR_GUIDE.md) and
[Claude procedure](H3_CLAUDE_ACCEPTANCE.md) retain their original revision scope.

Recorded: historical sole-writer revision/binary/lifetime, connectivity, exact
session/cwd, retained original12 of16 messages, two native user/assistant pairs,
normal completed status/result and unauthorized-read rejection. Missing acceptance:
explicit Chat call-order/account details, marker recall without marker in turn-2
input, independent same-key replay and live ADR-011 monitor-expiry reconciliation.
Permission-wait behavior is unexercised. See P4-EVID-01 for the criterion matrix,
archive limitations and future separately authorized test requirements. Stop with an explicit
BLOCKED/UNVERIFIED outcome when prerequisites fail; local SDK/TLS success is not
Chat interoperability. The H3 procedure's seven-tool expectation applies to its H3
candidate; an H4-A candidate requires eleven tools while preserving those seven.

Fixed-header capability is available on the maintainer's account (2026-11-03);
a community report of headers being ignored means the first live call must be a
read-only probe proving server-side authentication. The H3 credential lifetime
is at most 24 hours and Claude connector authentication cannot be edited, so each
run needs credential rotation and connector re-addition. Deployment/write gates
remain separate; this roadmap authorizes none.

**ADR-015: PROPOSED; review pending.** The previous security review encountered
an access restriction, as reported by the user. ADR-015 is not in the repository;
an uncommitted proposal is referenced by P4-EVID-01. Reading it does not establish acceptance or
an implementation decision. Do not bypass the restriction, reproduce restricted
experiments or expand security implementation. ADR-010/011 remain accepted within
their recorded scope; ADR-012/013 remain proposed. No missing ADR is inferred to
be accepted.

### P4-PERM — trusted authorization delivery feasibility gate

**Executed 2026-11-01: PERMISSION_FALLBACK_REQUIRED.** Direct in-chat native
approval is NO-GO because real-client and trusted human-action evidence is
NOT_VERIFIED; this is not proof that Claude Chat lacks elicitation support. The
gate below stays open for new evidence.
Investigate MCP elicitation and Multi Round-Trip Requests (MRTR), the negotiated
protocol/SDK versions, and the actual Claude Chat account/connector support.
Protocol specification, advertised capability, SDK fixture and real Claude Chat
user-confirmation behavior are distinct evidence levels. Verify each supported
request/reply and reconnect/cancellation/timeout behavior against official sources
and the installed versions before designing a native permission relay. Neither
elicitation nor MRTR support is asserted here.

The trusted authorization channel must provide a server-verifiable human action
bound to the exact pending request, principal, server/session/execution, project,
scope, choice and expiry, with replay protection. Document who authenticates the
human, how Bridge verifies that assertion and binding, and how revocation,
redaction and shared bearer limitations are handled. An authenticated MCP client
is not independently authenticated human intent; ordinary model-generated text
saying “the user approved” is never evidence. Generic MCP tool consent does not
establish native permission approval unless the exact action and trustworthy
binding have been independently demonstrated.

Present a safe, redacted request in the supervising Claude Chat conversation:
identity/correlation references, operation/tool, meaningful resource scope,
risk/reason, expiry and allow-once/deny choices. Untrusted repository, model and
native text is data, not instructions or authority. Redaction must not hide scope
needed for informed consent; if a safe sufficient description is impossible,
withhold relay and direct the user to the exact native request through a trusted
operator workflow. Never send secrets/raw credentials to Chat or audit.

Verify delivery while Chat is active and after it has completed a response.
**Do not assume automatic Chat wake-up, push notification or endless polling.**
Specify bounded active-turn polling and an explicit user follow-up/reconnect path
that re-discovers the still-pending exact request; expired approvals cannot revive.

Fail-closed alternative: if trustworthy user-confirmation delivery is unavailable,
disable remote permission response, retain safe read-only permission visibility,
report the exact pending target and require the user to act in the verified native
TUI/operator channel. Observe its resolution through authorized reads and continue
the same known execution when evidence permits. Native denial remains binding.
This fallback permits honest limited operation; it does **not** pass the required
Phase 5 Chat approval round-trip, which remains BLOCKED/UNVERIFIED. No extra
frontend, authorization service or LLM supervisor is implied.

## Phase 5 — Supervisor & Evidence Loop

**Status: partially prepared; product acceptance unverified.** Depends on Phases
1–4. Claude Chat remains the reasoning supervisor; H3's acceptance script and
ADR-011 provide foundations, not proof of an autonomous supervisor.

Missing acceptance: demonstrate Discover → Read → Plan → Send → Monitor →
Evaluate → Continue / Finish against measurable user criteria. Link every verdict
to correlated output plus relevant diff/test/log evidence; session idle and an
agent's success claim are insufficient. Demonstrate a justified corrective
follow-up in the same session, at most five correction iterations unless the
user specifies otherwise, an explicit polling/overall budget, and stop/escalation
for permission waits, missing evidence, disconnection or unknown outcomes. Never
replay uncertain work. Record evidence unavailable through current contracts as a
gap rather than claiming diff/test verification. Normal Chat does not guarantee
automatic wake-up after its response ends.

### P5-PERM — Permission Bridge milestone acceptance (not an MVP gate)

**BLOCKED pending P4-PERM evidence.** Since 2026-11-03 this is the acceptance
gate of the Permission Bridge milestone, not of the MVP; until it passes, the
fail-closed fallback applies and no Chat approval claim is made.
Existing-session continuation and its ordinary two-turn proof remain required.
In addition, demonstrate this actual human approval round-trip from real Claude
Chat, using an independently authorized test operation in an **existing OpenCode
session**:

**Existing session → original execution's native tool requests permission →
Chat surfaces the exact pending request safely → user explicitly approves once
through the verified trusted channel → Bridge validates and relays the native
reply → the original execution continues → correlated result and audit observed.**

Keep native server/session/workspace, execution/message and permission IDs plus
request generation, principal, decision/expiry and native outcome correlated.
Capture redacted real Chat presentation/human-action evidence and independent
native/audit evidence, exact connector/native/Bridge versions and revision.
One native reply, no new session/prompt, no automatic approval, and continued
monitoring must be demonstrated; an SDK fixture or model success claim is not a
pass. Verify evidence against the user's result criteria after native completion.

Required negative acceptance cases (planned tests, not results):

| Case | Required observation |
| --- | --- |
| User denial | Exact pending request receives deny only under verified authority; no allow, no prompt replay; resulting execution status is observed |
| Unauthorized principal | Denied without native reply or cross-principal request disclosure |
| Wrong session / execution / project / scope | Exact binding mismatch denied; no mutation of any pending request |
| Stale permission ID / changed request / expired consent | Denied despite matching display text; no replacement request selection |
| Duplicate decision | Same recorded decision observed, no second native reply; conflicting duplicate rejected |
| Native TUI resolves first | Native outcome wins; Bridge does not reply to a resolved/replaced request |
| Permission response lost | Delivery remains unknown until positive native reconciliation; no automatic resend or invented success |
| Bridge restart | No approval/grant hydration or old-consent reuse; fence/reconcile, preserve uncertainty and audit linkage |
| Native deny | Remains denied; no override, mode change, different prompt or session to bypass it |
| Connector without verified user-confirmation capability | Remote reply disabled, fail-closed native-user fallback reported; Chat round-trip gate remains blocked |
| Cancellation / timeout / Chat turn ends | No implicit allow or native abort; bounded stop and explicit resume/intervention path demonstrated |
| Untrusted “user approved” text / generic MCP tool consent | No independent native approval authority; no native reply |

For each case record expected/observed execution state, native reply count,
trusted authorization evidence or denial reason and secret-safe audit witnesses.
Distinguish a successful deny relay from a completed execution; native evidence
sets the eventual execution outcome. Do not prefill PASS or upgrade historical
Phase 4 evidence to satisfy this new requirement.

## Phase 6 — Claude Code Adapter

**Status: future; research only, installed capabilities unverified.** H4-D maps
here, not to the optional provisioning phase. See [H4 plan](H4_PROVISIONING_PLAN.md).

Acceptance before delivery: audit the installed supported interface; choose an
explicit Go adapter/runtime boundary; demonstrate exact-ID historical resume,
retained history and cwd, structured result correlation, bounded monitoring,
permission handling, concurrent-writer rejection, cross-project denial and crash
uncertainty. Historical resume must be distinguished from control of an actively
running terminal. A Python/Node runtime or any active-session control claim needs
its own supported design/evidence; neither is introduced by this roadmap.

Extend Permission Bridge to Claude Code **only after** verifying its actual
supported permission interface for the exact installed CLI/SDK version: request
identity, execution correlation, supported allow-once/deny reply, concurrency,
expiry/cancellation and reconnect semantics. Do not assume OpenCode endpoints,
an interactive prompt scrape or a proposed callback establishes support. An
unsupported interface leaves remote approval disabled. Prove the Phase 2/4/5
safety matrix independently for this adapter. Claude Code is a harness; its model
selection is limited to its verified supported capabilities and account policy.

## Phase 7 — Workspace Discovery, Selection, Launcher & Session Observatory

**Status: future product capability, outside the current core MVP.** H4-A is a
read-only backend foundation, not completion of these packages. Claude Chat
remains the primary interface; a frontend is optional, separately reviewed, and
not required for discovery or an explicitly authorized new native session.
Optional read-only discovery may be piloted earlier only if it does not delay
existing-session continuation or Phase 4/5 MVP gates; it grants no launch rights.

| Package | Scope and acceptance/security requirements |
| --- | --- |
| **7-A — Allowlisted repository/worktree discovery** | Bounded, read-only inventory under explicitly approved project roots. Verify canonical root, directory instance, Git common-dir/worktree identity and authorized metadata storage independently. No repository hooks/config execution, symlink/path alias trust, arbitrary filesystem scanning or ACL expansion. Show stale/missing/unsupported evidence; no automatic migration of existing session state to new worktrees |
| **7-B — Harness server, native session and execution inventory** | Join only through verified workspace and configured native authority/server identity plus native session and execution correlation. Distinguish running server, native session, attached TUI and actual execution. Repository path alone never proves an active execution; no process ownership/adoption from endpoint, PID or path. Enforce per-principal discovery/read grants and redact unauthorized candidates |
| **7-C — Installed harness/provider/model catalog and grounded recommendations** | Discover exact installed harness/version, enabled provider configuration, allowed/available models and supported capabilities without disclosing credentials. Label observed, declared, unavailable and unknown facts with source/version/time. Distinguish harness from model; OpenCode may run an authorized Claude model when its provider configuration supports it. Claude Code model choice is limited to verified supported capabilities. Ground recommendations in task, availability, capabilities and user constraints; do not invent compatibility, prices or availability. A recommendation never launches work or grants authority |
| **7-D — Explicitly authorized new session launcher** | Present exact principal, approved project/root/worktree, existing server or separately approved managed profile, harness/provider/model, intended work and scope for explicit user approval. Revalidate current identities and capabilities before creation. Separate create, discovery, prompt, permission-response and lifecycle grants; no automatic launch from recommendations. Require idempotency, audit and ambiguous-outcome reconciliation as detailed below |
| **7-E — Session Observatory** | Bounded session/execution monitoring, permission inbox, timeline and correlated evidence/history/diff views. Separate waiting, unresolved, stale, disconnected and terminal state; no success from idle alone. Inbox decisions use the same trusted channel and single server authority, never frontend-local approval logic. Preserve native history order and opaque pagination; ACL-check every item and linked child. Diffs identify workspace/baseline and distinguish current changes from proven execution artifacts; never execute repository content for display |

7-D retains the H4-B provisioning plan's distinct create grants, root/native
identity checks, durable creation-operation idempotency evidence, post-create
validation, protected exact-session grant publication and writer-ownership gates.
Define/review that durable operation contract before implementation; it is not
supplied by the current audit metadata, not a new database, and not historical
execution recovery. Same key/payload returns the recorded mapping; changed input
conflicts. Lost create response/restart retains uncertainty: reconcile before
another create, never blindly retry. Audit user approval, target/profile/model,
operation identity, attempt, returned native identity, grant/ownership and outcome
without secrets. Audit/policy/identity failure denies creation or quarantines an
already-created uncertain result; never auto-delete it or broaden access.

Use an approved existing server/workspace when supported; **new sessions do not
require managed sandboxing**. If managed process launch or isolation is explicitly
chosen, apply Phase 8/H4-C's separate authority, ownership and safety gates. Never
adopt an attached server or migrate/copy old session state/history automatically
to a new worktree; starting a new session does not continue an existing session.
Existing-session continuation keeps scheduling priority over all 7-D work.

Future UI/editor transport and authentication require review. Opening VS Code
requires an explicitly selected authorized workspace and user action, documented
local/remote behavior, and no arbitrary shell/executable or implied harness launch.
No launcher, new tool, frontend or production service is implemented here.

## Phase 8 — Optional Extensions

**Status: future, optional; no implementation authorization.** H4-B session
creation is planned under 7-D with its safeguards retained; H4-C managed lifecycle
and Optional Execution Isolation remain optional here. Other possible
extensions include durable execution storage/recovery, an external background
supervisor (not a second LLM supervisor in the current MVP), event streaming and
multi-harness aggregation when a concrete need justifies them. None is an MVP prerequisite by default.

Acceptance must be scoped per extension before coding. Phase 7-D/H4-B needs distinct
discovery/create/write grants, durable create idempotency and ambiguous-outcome
reconciliation. H4-C needs explicit process ownership and bounded safe lifecycle
handling; attached native servers must never be adopted implicitly. Durable
recovery needs crash/replay and retention semantics; a background supervisor needs
budgets, user intervention and evidence-based stop criteria. Preserve the existing
H4 plan's detailed prerequisites without enabling them.

## Open technical feasibility questions

These are unresolved gates, not assumptions or additional authorized tasks:

- Which exact OpenCode native interface exposes pending request identity and
  scoped allow-once/deny reply, and what atomic race/idempotency guarantees does
  the installed version provide? Can native evidence prove same-execution
  ownership and lost-response resolution without reissuing a reply?
- Does the actual Claude Chat connector support trustworthy elicitation/MRTR
  human decisions, including identity/binding, cancellation and later user
  follow-up? What evidence can Bridge independently verify beyond bearer access
  or model-authored arguments? If unavailable, the fail-closed fallback applies.
- What minimal permission decision/attempt audit contract and Mutation Gate
  integration preserve existing boundaries without treating journal replay as
  restored consent? Later 06D-B1/B2 changes are not covered by the approved
  06D-A reviews and need their own review before integration.
- Which supported Claude Code permission and model-selection interfaces are
  actually installed? How will unavailable capabilities be reported safely?
- Which authorized catalog sources and native identities can ground Phase 7
  recommendations/inventory, and how will create idempotency and grant publication
  resolve uncertain native creation without mandatory managed isolation?

## MVP definition

The MVP is accepted when a real Claude Chat conversation, through the
authenticated connector, satisfies HARNESS_BRIDGE_SPEC.md §13 against a
pre-existing OpenCode session on a recorded revision: discovery, history,
prompt 1, meaningful completion, result and evidence, prompt 2 in the same
session with marker recall (marker absent from prompt 2), unchanged history
prefix and working directory, and an accurate final report. Required negative
cases: duplicate submission (same-key replay returns the same IDs with zero new
native prompts), busy session, connection loss, timeout, unauthorized access and
interrupted or uncertain execution. Permission waits are detected and reported;
resolution uses the fail-closed native fallback. Live ADR-011 late completion is
recorded as observed or NOT_EXERCISED, never inferred.

## Execution plan 2026-11-03 → 2026-11-17

| Milestone | Scope | Phase | Exit evidence |
| --- | --- | --- | --- |
| R0 | Handoff verification, evidence preservation, this roadmap | — | Hashed evidence manifest; roadmap committed |
| R1 | CI baseline (PR #7), nested-module and multi-arch CI follow-up | Workflow | Exact-head CI on every PR |
| R2 | Integrate PR #3 → #4 → #10 → #9 with merge commits; #5/#6 superseded by #10 | 2, 3 | Reviewed SHAs reachable from `main`, CI green |
| R3a | Real Chat gap closure on the accepted H3 writer `4cb09fb` | 4 | Marker recall, same-key replay, call order, auth negatives |
| R3b | Integrated `main` in read-only mode through the real connector | 3, 4 | Eleven-tool discovery; seven-tool compatibility |
| R4 | CORE-02 PR, permission observation, fail-closed fallback | 2 (P2-PERM) | Reviewed PR; native reply disabled |
| R5 | Supervisor loop with negative cases | 5 | MVP definition above satisfied |

Status 2026-11-03: R0 complete except this commit; R1 PR #7 merged (`477180e`); R3a PASS.

Status 2026-11-04, updated against `main` `0499a23` (results only; plan above unchanged):

| Milestone | Status | Record |
| --- | --- | --- |
| R1 | Done | PR #7 (`477180e`), PR #13 CI-02 nested-module and amd64/arm64 CI (`abaf482`) |
| R2 | Done | PR #3 (`200368f`), #4 (`7ac2704`), #10 (`e84397d`), #9 (`eabb148`), #12 F-1 follow-up (`9ed25a3`) |
| R3a | PASS | `docs/evidence/R3A_EVIDENCE_2026-11-03.md`; ADR-011 live late completion NOT_EXERCISED |
| R3b | PASS with F-4 | `docs/evidence/R3B_EVIDENCE_2026-11-04.md`; F-4 fixed by PR #16 (`0499a23`) |
| R4 core | Partial | PR #14 CORE-02 merged (`749b08d`); the permission observer is not wired; native E2E NOT_VERIFIED |
| R5 | Pending | — |

Other merged: PR #15 FIX-FIXTURE-01 (`c49f091`), PR #18 FIX-FIXTURE-02
(`92c0b13`). Open: PR #22 FIX-FIXTURE-03, PR #23 AUDIT-01.

PR #10 superseded PR #5 and PR #6. Both were closed unmerged, and their changes
reached `main` only through #10's merge reconciliation (`877b247`). See
[D-F1](decisions/D-F1.md) for the PR #4 / F-1 decision and
[D-MERGE-DELEGATION](decisions/D-MERGE-DELEGATION.md) for the merge gate.

## Backlog (recorded 2026-11-04)

| Item | Phase | Note |
| --- | --- | --- |
| P3 audit logging | 3 | Add execution ID, replay flag and error code to the accepted/replay log line; PR #23 AUDIT-01 open |
| Native "stalled" liveness | 5 (R5) | Detect and report a native execution that stops making progress. ADR-017 option (a) accepted 2026-11-05 (D-ADR017-A): report-only `liveness` on execution status/result (CONTRACTS.md); R5 records the block per ADR-017 §4 and never acts on it automatically. Option (b) needs a live-evidence amendment; (c) rejected |
| Idempotency keys across writer restarts | 2 | Rule: never reuse idempotency keys across writer restarts |
| H4-A promotion gate | 3 / 7 | F-1 done (PR #12); F-4 done (PR #16); F-2 and F-3 open (below) |

### H4-A promotion gate: F-2 and F-3

Quoted verbatim from independent review R2-PR4-REVIEW (Worker A, 2026-11-03,
PR #4 head `0b3c319c96b0da0f921d63e8be6e5b063c66b0fc`, reviewed delta
`4cb09fb..0b3c319`). Locations refer to that head.

| ID  | Sev | Blocks | Location | Finding |
|-----|-----|--------|----------|---------|
| F-2 | P3 | writer promotion | internal/adapters/opencode/client.go (Agents, Status, Permissions via WithDirectory); discovery.go:54–56, :224 | Hypothesis, not verified: OpenCode bootstraps its per-directory instance on `?directory=`, which loads that repo's .opencode config, agents and plugins. The Bridge executes nothing itself, and the base already sent `directory` for get_session/history. H4-A adds directory-scoped /agent, status and permission calls for every ACL'd directory on each discovery. Verify before promotion. |
| F-3 | P3 | writer promotion | internal/repository/repository.go:140; storage_linux.go | The 2 s Inspect deadline is checked between syscalls, but a blocking stat/open (hung NFS/FUSE) is not interruptible. One worker stalls the discovery past its 10 s deadline. |

#### F-2 verification note (H4A-F2-VERIFY, 2026-11-05, Worker C)

Static source reading only; OpenCode was not run and no Bridge or OpenCode call
was made. Upstream `anomalyco/opencode` tag v1.19.4 = commit
`148842ef7cb1bc34192b3fff57d0f9721ab9c801`; paths below are under
`packages/opencode/src/` unless prefixed `core/` (`packages/core/src/`).

**Mechanism: confirmed.**

- Routing. `?directory=` (or `x-opencode-directory`, else the server cwd)
  selects the instance directory for requests without a session ID
  (`server/routes/instance/httpapi/middleware/workspace-routing.ts:86-88`,
  `:160-184`); a `/session/:id…` request uses the stored `session.directory`
  instead (`:182`, `:212-236`), or its workspace target when the session has a
  `workspaceID` (`:150-157`). `/session/status` never resolves a session
  (`server/shared/workspace-routing.ts:20-21`).
- Instance load. `InstanceContextMiddleware` calls `InstanceStore.load` for that
  directory (`…/middleware/instance-context.ts:23-35`). It is attached to the
  session (including `/session/status`), permission and instance (including
  `/agent`) groups (`…/groups/session.ts:452`, `…/groups/permission.ts:51`,
  `…/groups/instance.ts:196`). `load` boots once per resolved directory and
  caches the result until the instance is disposed
  (`project/instance-store.ts:108-124`, boot `:45-63`).
- Bootstrap (`project/bootstrap.ts:30-43`): `config.get()`, then
  `plugin.init()`, then `lsp`, `format`, `vcs`, `snapshot` and `project` init.
- Config (`config/config.ts:420-478`, `config/paths.ts:10-41`,
  `core/fs-util.ts:168-182`). It walks from the directory up to the worktree,
  inclusive, merging every `opencode.json(c)`, and also uses every `.opencode`
  directory on that path plus `~/.config/opencode`, `~/.opencode` and
  `OPENCODE_CONFIG_DIR`. For each such directory it:
  - writes a `.gitignore` if absent (`config.ts:309-324`);
  - starts a background npm install of `@opencode-ai/plugin` and the
    directory's declared dependencies, using that directory's npm config
    (`config.ts:452-472`, `core/npm.ts:88-101`, `:147-196`). Arborist runs
    with `ignoreScripts: true`, so no lifecycle scripts run, but packages are
    fetched and `node_modules` is written when the directory is writable;
  - loads agents and commands, and globs `{plugin,plugins}/*.{ts,js}` as
    plugins (`config/plugin.ts:18-29`). `OPENCODE_DISABLE_PROJECT_CONFIG`
    skips the project-level walk (`config.ts:420`, `paths.ts:27`).
- **Plugins execute at bootstrap, not on first use.** `Plugin.init` forces the
  per-instance state (`plugin/index.ts:304-306`). That state imports each
  configured or auto-discovered plugin module (`plugin/loader.ts:135-140`) and
  calls its `server(input)` factory, then its `config` hook
  (`plugin/index.ts:114-124`, `:181-253`). The runtime flag `pure` skips
  external plugins (`:181`).
- Worktree. For a non-git directory OpenCode uses the `global` project with
  worktree `/` (`project/project.ts:217`, `:309`). The search for the R5 test
  directory therefore covers `/opt/harness-bridge-test/workspace`,
  `/opt/harness-bridge-test`, `/opt` and `/`. H4_A_EVIDENCE.md recorded
  `id=global, worktree=/` for that directory.
- Other bootstrap work:
  - VCS state is forked at init; for git projects it runs branch queries
    (`project/vcs.ts:305-341`).
  - The snapshot state starts an hourly cleanup loop (`snapshot/index.ts:760-766`).
  - LSP servers are configured at init but spawned only when a file is used
    (`lsp/lsp.ts:145-205`, `:309-311`).
  - MCP is not part of bootstrap.
  - **UNVERIFIED:** whether a native recursive file watcher is started per
    instance at bootstrap, whether anything reached from `/agent`, `/permission`
    or `/session/status` initializes MCP state, and whether 1.19.4 disposes idle
    instances (which would cause a re-bootstrap on the next call).

**What H4-A adds: no new class of trigger, but more calls.**

- H3 (`4cb09fb`) `list_sessions` already called `authorized()`, and therefore
  `GET /session/:id`, for every ACL'd session (`internal/mcp/server.go:71-86`
  @`4cb09fb`). That request is routed to `session.directory` and boots that
  instance. `send_prompt` preflight already sent directory-scoped
  `/session/status` and `/permission` (`internal/execution/manager.go:236`,
  `:258` @`4cb09fb`).
- H4-A discovery calls `authorized()` for every ACL'd session first
  (`internal/mcp/discovery.go:210`). Its directory-scoped status, permission and
  `/agent` calls then go to the same `session.directory`, whose instance is
  already loaded and cached.
- H4-A adds frequency and reach rather than a new trigger: `find_sessions`,
  `list_repositories`, `get_repository` and `get_harness_capabilities` also
  reach it, including on a `BRIDGE_READ_ONLY=1` Bridge (as pre-H4
  `list_sessions` already did). So a read-only listing still runs that
  workspace's plugins whenever the instance is not yet loaded, for example
  after an OpenCode restart.
- One narrow new case: for a workspace-bound session (non-empty `workspaceID`),
  `/session/:id` routes to the workspace target, while H4-A's `?directory=`
  calls load a local instance for `session.directory` that may not otherwise
  exist. H3 `send_prompt` preflight already did this for the prompted session
  only.

**Risk for the R5 test instance (only ACL'd directory
`/opt/harness-bridge-test/workspace`).** It is low **if** none of the searched
paths holds a plugin or a config declaring one. Confirming this needs the
following path listings, run by the operator as the OpenCode service user. They
list names only and do not read file contents:

```sh
for d in /opt/harness-bridge-test/workspace /opt/harness-bridge-test /opt /; do
  ls -ld "$d/.opencode" "$d/opencode.json" "$d/opencode.jsonc" 2>/dev/null
  ls -la "$d/.opencode/plugin" "$d/.opencode/plugins" 2>/dev/null
done
ls -la ~/.config/opencode ~/.config/opencode/plugin ~/.config/opencode/plugins ~/.opencode 2>/dev/null
systemctl show -p Environment <opencode-unit> | tr ' ' '\n' | grep -E '^OPENCODE_CONFIG(_DIR|_CONTENT)?=' | cut -d= -f1
```

Expected result: no `.opencode`, `opencode.json` or `opencode.jsonc` at the four
levels; only the operator's own global config; no `*.ts`/`*.js` in any `plugin`
or `plugins` directory. A config file's `plugin` array cannot be checked by
listing alone. Whether the operator's global `opencode.jsonc` (R3a hash
`9f3b7417…5457`) declares plugins is therefore **UNVERIFIED**; the operator
decides whether to inspect that key.

**Verdict: F-2 mechanism confirmed. It is mitigated, not closed.**

- Not a new H4-A trigger. The same code already runs when that workspace is
  opened in OpenCode or reached by pre-H4 `list_sessions`/`get_session`.
- R5 promotion may proceed once the listing above is clean. Until then, F-2
  stays open for R5.
- Discovery must be documented as able to bootstrap, and so run plugins of,
  every ACL'd workspace, including on read-only Bridges. ACL entries should be
  limited to workspaces whose `.opencode` content the operator trusts.

**Proposed follow-ups (not implemented here):**

1. Document this boundary in CONTRACTS.md and the operator runbook.
2. Optionally skip directory-scoped status, permission and `/agent` calls for
   workspace-bound sessions. Their state is already `unknown` (FIX-STATE-01b),
   so this removes the one new bootstrap case.
3. Operator choice: run the test OpenCode with
   `OPENCODE_DISABLE_PROJECT_CONFIG` if project-level config is not wanted.
   This changes OpenCode behaviour for all users of that instance.

#### F-2 correction and workspace routing (F2-WORKSPACE-ROUTE-VERIFY, 2026-11-05, Worker C)

The note above says `/session/:id` routes a workspace-bound session to its
workspace target, and it calls H4-A's `?directory=` reads the one new bootstrap
case. **Both claims are corrected here.** OpenCode v1.19.4 keeps every
`GET /session…` request on the local control plane
(`packages/opencode/src/server/shared/workspace-routing.ts:5-18`;
`…/middleware/workspace-routing.ts:177-184`). The ACL lookup and the history
reads therefore already load `session.directory` for workspace-bound sessions,
as they have since H2. PR #45 does not change the bootstrap surface. It removes
reads that may describe an instance that does not serve the session.
Classification, `?workspace=` routing and the proposed design for discovery and
the `send_prompt` preflight are in `docs/design/F2_WORKSPACE_ROUTING.md`.

Writer promotion of H4-A/H4-B code remains NO-GO until its own acceptance;
Phase 6 is limited to a feasibility spike; Phases 7–8 are out of scope.

## Status 2026-11-05

Recorded against `main` `da0fbd99372f3e8bf86c55ec2b4723727143a7dd` (DOC-STATUS-02,
issue #69). Append only: the earlier status tables and quoted texts above are
unchanged. Merge SHAs come from `gh api repos/nevzatcirak/harness-bridge/pulls/<n>`
(`merge_commit_sha`). Gate records are the Planning Desk merge-record comments on
each PR.

### Merged since the 2026-11-04 status

| PR | Task | Merge SHA |
| --- | --- | --- |
| #16 | FIX-STATE-01, F-4 (idle inferred from absence) | `0499a23055d480718796b443c97cd378c0867f46` |
| #22 | FIX-FIXTURE-03 (setup/probe mode guards) | `34541b1933b845f66a47f39eb16d40c1b9b4791a` |
| #23 | AUDIT-01 (structured audit line per mutating call) | `692992dbf78d72861db989ee7b4b1c580795b1c5` |
| #29 | ADR-017 PROPOSED (native stall liveness, report-only) | `b27b606574d52e0501f9800beb8ff24bad53101c` |
| #31 | FIX-FIXTURE-04 (fd-3 close test, guard wording, userns skip) | `4699d16aec0053d55b3ddf234176703b41a32d57` |
| #39 | DOC-AUDIT-NOTE (SDK unmarshal rejection path) | `8f55637000724bf3eebfe595e59374288361d0ad` |
| #40 | H4A-F2-VERIFY (F-2 verification note) | `8b88ee1dcbc76d34c4a309d1322632af856db8d4` |
| #41 | H4-A F-3 part 1 (`repository.Inspect` non-blocking) | `7adbff6a2c8ca5b3001523d89528c2f65c065e45` |
| #45 | H4A-F2-FOLLOWUP (skip directory reads for workspace-bound sessions) | `1ac24366bd705cb5ae552a10602f1c9f69bc63e7` |
| #50 | F2-WORKSPACE-ROUTE-VERIFY (workspace routing note) | `5b1efdb8b96d9dde9e47914bce76481e5945375c` |
| #53 | EXEC-FS-BOUND-DESIGN (design proposal) | `5201217cb9a58963d9bf57e0cd97bb4bf6c34a01` |
| #55 | H4-A F-3 part 2 (`Policy.AllowsContext`, `internal/bounded`) | `1c0d2d36256b95694010f003e4b6ed40fbdb652d` |
| #56 | ADR-017 option (a) ACCEPTED and implemented (D-ADR017-A) | `5758c4d4e60185d97e2d8e5edbf8227d636de673` |
| #57 | TEST-HARDENING-01 | `f95865efae375f914c4f86f04b12891400ad09fa` |
| #65 | TEST-HARDENING-02 | `da0fbd99372f3e8bf86c55ec2b4723727143a7dd` |

**Open:** PR #67 EXEC-FS-BOUND-IMPL (head `2e7d42dfe24066f8dc5cd5f36606e59e64baccc7`
when this was written; review R5-PR67-REVIEW, issue #68).

### H4-A promotion gate

| Item | Status | Record |
| --- | --- | --- |
| F-1 | Done | PR #12 (`9ed25a377cd6af768ed2947d44df8642c2324a30`) |
| F-2 | Mechanism confirmed; workspace-bound reads skipped; closed for the R5 test instance; general residual stays | Mechanism: #40. Workspace-bound directory reads skipped: #45. Closed for the R5 test instance by the operator listing ([issue #35](https://github.com/nevzatcirak/harness-bridge/issues/35#issuecomment-6653237061)). General residual: discovery bootstraps ACL'd workspaces, so the ACL must hold only trusted workspaces (CONTRACTS.md, "Discovery bootstraps native instances") |
| F-3 | Closed for `internal/mcp` and `internal/security`; execution part open | #41 and #55 ([merge record #55](https://github.com/nevzatcirak/harness-bridge/pull/55#issuecomment-6654139993)); the `internal/execution` part is PR #67 (open) |
| F-4 | Done | PR #16 (`0499a23`); confirmed live in R5 Step 2 preflight ([issue #35](https://github.com/nevzatcirak/harness-bridge/issues/35#issuecomment-6654185502)) |

### R5

**IN PROGRESS. The MVP is not accepted.**

| Step | Status | Record |
| --- | --- | --- |
| 1. Operator gate: writer candidate `692992d` installed | Done | [issue #35 operator records](https://github.com/nevzatcirak/harness-bridge/issues/35#issuecomment-6653237061): sha256 `d9837334…63b7`, `vcs.revision=692992dbf78d72861db989ee7b4b1c580795b1c5`, `vcs.modified=false` |
| 2. Read-only preflight | PASS (F-4 live) | [issue #35 preflight](https://github.com/nevzatcirak/harness-bridge/issues/35#issuecomment-6654185502) |
| 3. Acceptance run | Passed except the interrupt, which was re-run and passed (D′) | As stated by the Planning Desk in issue #69. No GitHub record of the run had been posted when this was written. |
| 4. Connection loss | Pending | — |
| Corroboration | Pending | — |

### Backlog (P3 items still open from gate records)

Copied from the Planning Desk merge records and the review they cite. Items
closed since are listed below the table.

| Sev | Item | Source |
| --- | --- | --- |
| P2 | F-2 residual: `send_prompt` preflight (`internal/execution`) still does directory-scoped Status/Permissions reads for workspace-bound sessions. A candidate fix is `?workspace=<id>` routing (unverified). | [#45 merge record](https://github.com/nevzatcirak/harness-bridge/pull/45#issuecomment-6651375298); design in `docs/design/F2_WORKSPACE_ROUTING.md` (#50) |
| P3 | Local-worktree workspace sessions are served by the directory instance, so `unknown` drops correct state there (safe direction). Qualify the premise to "remote or moved workspace sessions", or move to `?workspace=`. | [#45 merge record](https://github.com/nevzatcirak/harness-bridge/pull/45#issuecomment-6651375298) |
| P3 | `bounded` select: check `ctx.Err()` first, because an already-cancelled caller can randomly get a real result (not fail-open). This explains a rare flake in `TestWorktreeIdentityAndBounds`. | [#55 merge record](https://github.com/nevzatcirak/harness-bridge/pull/55#issuecomment-6654139993) item 3; addressed in PR #67 (open) |
| P3 | `//go:linkname` references evade the identifier lint (hostile code only). | [#57 merge record](https://github.com/nevzatcirak/harness-bridge/pull/57#issuecomment-6653576816); review [#60](https://github.com/nevzatcirak/harness-bridge/issues/60#issuecomment-6653338641) P3-1 |
| P3 | The CI reason match runs per output event, so a reason split across events would fail CI even though nothing is wrong. It never passes silently; note only. | [#57 merge record](https://github.com/nevzatcirak/harness-bridge/pull/57#issuecomment-6653576816) ("one more P3 on the CI assertion"); review [#60](https://github.com/nevzatcirak/harness-bridge/issues/60#issuecomment-6653338641) P3-2 |

**Closed since recorded:**
- #45's "no test for a workspace-bound session with a held guard (mutant M5 survives)" was closed by #57 ([merge record](https://github.com/nevzatcirak/harness-bridge/pull/57#issuecomment-6653576816)).
- #55's P3 items 1, 2 and 4 were closed by #65 ([merge record](https://github.com/nevzatcirak/harness-bridge/pull/65#issuecomment-6656228007)).

## R5 result 2026-11-06

Source: [`docs/evidence/R5_EVIDENCE_2026-11-05.md`](evidence/R5_EVIDENCE_2026-11-05.md)
(sha256 `c9e6ba73fbbee5e075e8376cc08f3252467409273727dd16a48e7f46f605c972`); runbook
[`docs/evidence/R5_RUNBOOK.md`](evidence/R5_RUNBOOK.md)
(sha256 `b94ff69477082888791e2173b853d676852bb58c708da4604c8de662577ab1bb`).
Results per criterion are in the evidence file's "Verdict (Planning Desk)" table.

Verdict table row headings (quoted verbatim):

- Real Claude Chat, authenticated connector, pre-existing session, recorded revision
- Discovery, history (incl. pagination), identity, cwd
- Prompt 1 with meaningful completion, result and evidence; acceptance criteria evaluated
- Prompt 2 in the same session; marker recall without the marker in the prompt
- Unchanged history prefix and working directory
- Duplicate submission → same IDs, zero new native prompts
- Busy session
- Unauthorized access
- Interrupted execution
- Uncertain execution and connection loss
- Timeout
- Accurate final reports
- Permission wait detected and reported (fail-closed native fallback)
- ADR-011 live late completion

Final verdict (quoted verbatim):

> **R5: PASS. MVP accepted on revision `692992d`, with ONE recorded gap:** live permission-wait detection was not exercised. Closing that gap needs a short supplementary run (R5b): configure the test instance with `permission.bash: ask`, send one prompt, observe `waiting_for_permission`, and have the human reject it natively. The maintainer decides whether R5b is required before the MVP is declared, or is tracked as a follow-up.

## R5b result 2026-11-06 — MVP accepted incl. R5b

Source: [`docs/evidence/R5B_EVIDENCE_2026-11-06.md`](evidence/R5B_EVIDENCE_2026-11-06.md)
(sha256 `ccf834ceb6fbb870ff455d4b02fd5fddfa06e4173140e88c73b057764b49c785`); runbook
[`docs/evidence/R5B_RUNBOOK.md`](evidence/R5B_RUNBOOK.md)
(sha256 `36280fe4ba9851a6403d5d365bec2594e1f0697277d0827fe6567f37448fa5fe`).
Results per criterion are in the evidence file's "Verdict" table.

Verdict table criteria (quoted verbatim):

- `waiting_for_permission` observed on the execution while pending
- `waiting_for_permission` observed on the session (`get_session`)
- The Bridge made no approval
- The human rejected natively
- Post-rejection state is terminal or `unknown`, never success
- `R5B-PERM-PROBE` never produced
- Guard consistent with the state
- Journal corroborates
- Native DB corroborates

Final verdict (quoted verbatim):

> **R5b: PASS. MVP accepted on revision `692992d`, including R5b.** The single R5 gap, live permission-wait detection, is closed, and every pass criterion is corroborated by the journal and by native records.

Findings (rows quoted verbatim from the evidence file's findings table):

| ID | Finding | Severity |
|---|---|---|
| F-5 | Discovery reports `idle` while admission refuses with `SESSION_BUSY: unfinished native assistant message`. Discovery does not consider unfinished assistant messages. | P2 |
| F-6 | An OpenCode restart during a tool call leaves an orphan assistant message. Native abort cannot clear it, and the only recovery is native `DELETE /session/:id/message/:messageID`. | P2 |
| F-7 | A native permission rejection is not mapped to a terminal state. The execution stays `unknown` (`OBSERVATION_UNAVAILABLE` → `MONITOR_EXPIRED`) and the guard stays held. Upstream signals must be verified before a typed terminal outcome (e.g. `failed` / `PERMISSION_REJECTED`) is proposed. | P2 |
| P3-perm | Bridge status does not expose which permission is pending (tool, pattern). | P3 |

Follow-ups (none decided here; every ADR below stays PROPOSED until the
maintainer decides):

- F-5: issue [#77](https://github.com/nevzatcirak/harness-bridge/issues/77), PR [#82](https://github.com/nevzatcirak/harness-bridge/pull/82).
- F-6: issue [#79](https://github.com/nevzatcirak/harness-bridge/issues/79), PR [#84](https://github.com/nevzatcirak/harness-bridge/pull/84).
- F-7: issue [#78](https://github.com/nevzatcirak/harness-bridge/issues/78), PR [#83](https://github.com/nevzatcirak/harness-bridge/pull/83) (ADR-018, PROPOSED).
- Connector token lifetime: issue [#80](https://github.com/nevzatcirak/harness-bridge/issues/80), PR [#81](https://github.com/nevzatcirak/harness-bridge/pull/81) (ADR-019, PROPOSED).

## Finding F-6 2026-11-06: orphan assistant message recovery

An OpenCode restart during a running tool call leaves an assistant message
without `time.completed`. Native abort does not clear it, and the Bridge then
refuses every `send_prompt` for the session with `SESSION_BUSY: unfinished native
assistant message` (P2, R5/R5b). Detection and the maintainer-only native
deletion procedure are in
[`docs/deploy/ORPHAN_MESSAGE_RECOVERY.md`](deploy/ORPHAN_MESSAGE_RECOVERY.md).
The discovery side is F-5 (PR #82).

## Status 2026-11-07 (consolidated)

Recorded against `main` `22264de340afde912bdf2161ac66a7478581cd6a` (DOC-ROADMAP-STATUS,
issue #106). Append only: no earlier section is edited. Statements above that are
no longer true are listed under "Superseded statements" with the current fact.
Merge SHAs come from `gh pr view <n> --json mergeCommit` and match
`git log --merges` on `main`.

### Current state

- **MVP accepted** on revision `692992d`, including R5b:
  - [`docs/evidence/R5_EVIDENCE_2026-11-05.md`](evidence/R5_EVIDENCE_2026-11-05.md)
    ("R5: PASS. MVP accepted on revision `692992d`, with ONE recorded gap");
  - [`docs/evidence/R5B_EVIDENCE_2026-11-06.md`](evidence/R5B_EVIDENCE_2026-11-06.md)
    ("R5b: PASS. MVP accepted on revision `692992d`, including R5b"), which
    closes that gap (live permission-wait detection).
- **`main`** is `22264de` (PR #99) when this section was written. It contains
  every change listed below.
- **Live writer:** still the R5 binary built from `692992d`
  (`/usr/local/libexec/harness-bridge-r5`, sha256 `d9837334…63b7`, as recorded in
  the R5 and R5b evidence). It predates F-3 bounding (#55, #67), ADR-017 (a)
  (#56), F-5 (#82), ADR-018 (d) (#99) and ADR-019 (2) (#100). Promotion of `main`
  (H4) is pending and needs its own acceptance.

### Merged since 2026-11-05

| PR | Merged (UTC) | Merge SHA | Scope |
| --- | --- | --- | --- |
| [#70](https://github.com/nevzatcirak/harness-bridge/pull/70) | 2026-11-05 16:27 | `e605d67dd1ec9deee3f24f8b26b5da51caee4ead` | DOC-STATUS-02: "Status 2026-11-05" section |
| [#67](https://github.com/nevzatcirak/harness-bridge/pull/67) | 2026-11-05 18:07 | `868675a4ea52dd9d8adab6a0ea5037550f1419e3` | EXEC-FS-BOUND-IMPL: bounded workspace filesystem checks in `internal/execution` (F-3 execution part); early `ctx.Err()` in `bounded.Do` |
| [#75](https://github.com/nevzatcirak/harness-bridge/pull/75) | 2026-11-06 05:19 | `fb298ed0a5457f3cf0aeb8ba5b3efe7019f36e0d` | DOC-R5-EVIDENCE: R5 evidence and runbook; "R5 result 2026-11-06" |
| [#82](https://github.com/nevzatcirak/harness-bridge/pull/82) | 2026-11-06 11:19 | `15dc66faafe51da79349aefb6fd5ca926fa4b9f3` | F-5: discovery reports `unknown` and `native_unfinished_message` for an unfinished native assistant message; shared `opencode.UnfinishedAssistant` |
| [#81](https://github.com/nevzatcirak/harness-bridge/pull/81) | 2026-11-06 11:19 | `50ba68805c652d68ffc02dde52dd54af424b6976` | ADR-019 connector token lifetime (merged as PROPOSED) |
| [#83](https://github.com/nevzatcirak/harness-bridge/pull/83) | 2026-11-06 11:22 | `42f128d1c44e632bb3f996fae94848ff5080c78f` | ADR-018 F-7 permission rejection outcome (merged as PROPOSED) |
| [#90](https://github.com/nevzatcirak/harness-bridge/pull/90) | 2026-11-06 12:15 | `c9ce216c3c05f50aaeda0dffff2c7580e9baed48` | DOC-R5B-EVIDENCE: R5b evidence and runbook; "R5b result … MVP accepted incl. R5b" |
| [#93](https://github.com/nevzatcirak/harness-bridge/pull/93) | 2026-11-06 13:05 | `6bb0862a72734141c5fa974550a5362babb499d8` | F-5 review follow-ups: ErrBusy test, guard short-circuit, BACKLOG entries |
| [#84](https://github.com/nevzatcirak/harness-bridge/pull/84) | 2026-11-06 13:48 | `9f9775268fba7bb843a6877094e10d2c06fd71e8` | F-6: `docs/deploy/ORPHAN_MESSAGE_RECOVERY.md` (maintainer-only native deletion), review fixes |
| [#100](https://github.com/nevzatcirak/harness-bridge/pull/100) | 2026-11-06 21:11 | `7280906383bf1308a3c82954df1b08228f690df2` | ADR-019 option (2) accepted and implemented: `BRIDGE_TOKEN_MAX_LIFETIME` (default 24h, ceiling 720h) |
| [#99](https://github.com/nevzatcirak/harness-bridge/pull/99) | 2026-11-06 21:14 | `22264de340afde912bdf2161ac66a7478581cd6a` | ADR-018 option (d) on (a) accepted and implemented: report-only `permission_rejection` evidence |

### ADR status

| ADR | Status | Record |
| --- | --- | --- |
| ADR-015, ADR-016 | Referenced in `docs/` but absent from `docs/adr`; treated as reserved | — |
| ADR-017 | ACCEPTED, option (a) (report-only liveness) | [D-ADR017-A](decisions/D-ADR017-A.md); #56 |
| ADR-018 | ACCEPTED, option (d) built on (a); §5 (pending-permission detail, P3) still PROPOSED | issue [#97](https://github.com/nevzatcirak/harness-bridge/issues/97); #99 |
| ADR-019 | ACCEPTED, option (2) | issue [#98](https://github.com/nevzatcirak/harness-bridge/issues/98); #100 |
| ADR-020 | PROPOSED (guard recovery without a Bridge restart) | PR [#107](https://github.com/nevzatcirak/harness-bridge/pull/107) (open) |

### Superseded statements

Each quote below is unchanged in its section; the current fact replaces it.

1. "Status 2026-11-04 …" table: "| R5 | Pending | — |". **Now:** R5 PASS
   (2026-11-06) and R5b PASS; MVP accepted on `692992d` (sections "R5 result
   2026-11-06" and "R5b result 2026-11-06").
2. Same section: "Open: PR #22 FIX-FIXTURE-03, PR #23 AUDIT-01." **Now:** both
   merged: #22 `34541b1`, #23 `692992d` (table in "Status 2026-11-05").
3. "Backlog (recorded 2026-11-04)": "PR #23 AUDIT-01 open". **Now:** merged
   (`692992d`); the AUDIT-01 line is the live writer's journal format.
4. "Backlog (recorded 2026-11-04)": "F-2 and F-3 open (below)". **Now:** F-3 is
   closed (#41, #55, #67). F-2 is mitigated, and its P2 residual stays open (see
   "Open items").
5. "Status 2026-11-05": "**Open:** PR #67 EXEC-FS-BOUND-IMPL". **Now:** merged
   (`868675a`).
6. "Status 2026-11-05" → "H4-A promotion gate", F-3 row: "execution part open …
   PR #67 (open)". **Now:** closed by #67.
7. "Status 2026-11-05" → "R5": "**IN PROGRESS. The MVP is not accepted.**", and
   "4. Connection loss | Pending" / "Corroboration | Pending". **Now:** R5 PASS.
   Step 4 attempt 2 passed (typed `unknown`, no promotion or resubmission), and
   corroboration passed (R5-CORROBORATE, issue #73), as recorded in the R5
   evidence file. The MVP is accepted including R5b.
8. "Status 2026-11-05" → "Backlog (P3 …)": "`bounded` select: check `ctx.Err()`
   first … addressed in PR #67 (open)". **Now:** closed by #67 (`bounded.Do`
   returns `ErrTimeout` for an already-cancelled caller).
9. "R5b result 2026-11-06": "every ADR below stays PROPOSED", "(ADR-018,
   PROPOSED)", "(ADR-019, PROPOSED)". **Now:** ADR-018 is ACCEPTED ((d) on (a),
   §5 PROPOSED) and ADR-019 is ACCEPTED (option (2)). The F-5 (#82) and F-6 (#84)
   PRs listed there are merged.
10. "Finding F-6 2026-11-06": "The discovery side is F-5 (PR #82)." **Now:**
    #82 is merged (`15dc66f`).

### Open items (deduplicated)

| Sev | Item | Status | Record |
| --- | --- | --- | --- |
| P2 | F-2 residual: `send_prompt` preflight (`internal/execution`) still does directory-scoped status and permission reads for workspace-bound sessions; `?workspace=` routing unverified | Open | #45 merge record; `docs/design/F2_WORKSPACE_ROUTING.md` (#50) |
| P2 | Guard held on `unknown` until a Bridge restart (ADR-010); R5, R5b and F-6 each needed a restart | Proposal open | ADR-020, PR [#107](https://github.com/nevzatcirak/harness-bridge/pull/107) |
| P3 | `reconcile.go:396-399` reports `running` on an explicit native `idle` without terminal proof (unreachable on OpenCode 1.19.4) | Open | `docs/BACKLOG.md` (#93); #99 left it unchanged |
| P3 | No test for a guard released between `needsHistory` and `view` (fail-closed today; mutation Y1 survives) | Open | R93-REVIEW [#95](https://github.com/nevzatcirak/harness-bridge/issues/95) finding 1 |
| nit | #95 nit 2: the busy-slot test depends on timing | Open | [#95](https://github.com/nevzatcirak/harness-bridge/issues/95) |
| nit | F-6 runbook: "on every connection" wording (`ORPHAN_MESSAGE_RECOVERY.md:120-122`); no ready GET command in Step 2.1 | Open | R84-REREVIEW [#96](https://github.com/nevzatcirak/harness-bridge/issues/96) nits 1–2 |
| nit | ADR-019 implementation: startup error order changed; no deterministic test at the exact expiry boundary | Open | R100-REVIEW [#102](https://github.com/nevzatcirak/harness-bridge/issues/102) nits 1–2 |
| P3 | Process: `go test -race -count=20 ./internal/execution` takes about 633 s; gate commands need `-timeout 30m` | Open | R99-REVIEW [#101](https://github.com/nevzatcirak/harness-bridge/issues/101) finding 2 |
| nit | ADR-018 block can appear while native is still `busy` / `retry`; not stated in CONTRACTS | Open | [#101](https://github.com/nevzatcirak/harness-bridge/issues/101) finding 1 |
| P3 | `//go:linkname` references evade the identifier lint (hostile code only) | Open | #57 merge record; review #60 P3-1 |
| P3 | CI reason match per output event: a split reason fails CI (never passes silently) | Open | #57 merge record; review #60 P3-2 |
| P3 | Local-worktree workspace sessions report `unknown` where the directory instance could answer (safe direction) | Open | #45 merge record |
| — | ADR-011 live late completion | NOT_EXERCISED (R3a, R5, R5b) | R5 and R5b evidence |
| — | ADR-018 `native_event` unreachable until the permission observer is wired | Disclosed | #101 finding 3; CONTRACTS ADR-018 section |
| — | Research: P6 Claude Code adapter feasibility | In progress (Worker B) | issue [#103](https://github.com/nevzatcirak/harness-bridge/issues/103) |
| — | Research: P4-PERM trusted human approval channel from Claude Chat | In progress (Worker C) | issue [#104](https://github.com/nevzatcirak/harness-bridge/issues/104) |

**Closed since the 2026-11-05 list:**
- The `bounded` `ctx.Err()` P3 was closed by #67.
- The #85 findings 1–2 (ErrBusy test, guard short-circuit) were closed by #93.
- The #88 findings 1–4 (F-6 runbook) were closed in #84, as confirmed by
  [#96](https://github.com/nevzatcirak/harness-bridge/issues/96).
- The #86 findings 1–2 (ADR-018 wording and citation) were closed by #99.

### Next phases (in order)

1. Research: P6 (#103) and P4-PERM (#104).
2. H4 promotion of `main` as the live writer, with its own acceptance run
   (the live writer is still `692992d`).
3. ADR-020 implementation, if the maintainer accepts it.
4. Phase 7 (workspace discovery, selection, launcher, session observatory),
   later.

## H4 result 2026-11-10 — main 9db1c1d promoted as the live writer

Quoted verbatim from [`docs/evidence/H4_EVIDENCE_2026-11-10.md`](evidence/H4_EVIDENCE_2026-11-10.md):

> **H4: PASS.** The independent rebuild matched (#138), and the native DB, filesystem and journal corroborate the run (#139). Main `9db1c1d` is the live writer, with:

| ID | Finding | Priority |
|---|---|---|
| H4-F1 | `interrupt_execution` returns the execution state at request time (`running`). Clients must poll for the terminal state. CONTRACTS does not say this explicitly. Proposal: document it, or return an explicit `interrupt_requested` marker. | P3 |
| H4-F2 | An aborted bash part reads `status: "completed"` with `exit: null` and "User aborted the command". The Bridge correctly reports `interrupted`, but a client that reads native parts alone could be misled. Document it. | P3 |

Links:

- Evidence: [`docs/evidence/H4_EVIDENCE_2026-11-10.md`](evidence/H4_EVIDENCE_2026-11-10.md); runbook: [`docs/evidence/H4_RUNBOOK.md`](evidence/H4_RUNBOOK.md)
- PR [#141](https://github.com/nevzatcirak/harness-bridge/pull/141) (P6 spike evidence, merged `4648ca0`)
- Issues [#137](https://github.com/nevzatcirak/harness-bridge/issues/137), [#138](https://github.com/nevzatcirak/harness-bridge/issues/138), [#139](https://github.com/nevzatcirak/harness-bridge/issues/139)

Live writer: harness-bridge-h4 sha256 45fd5069…e854 (main 9db1c1d), token max lifetime 168h, ADR-020 admin socket enabled.
