# H4-B security continuation — actual evidence, 2026-10-25 UTC

**Code/fixture hardening PASS; full H4-B acceptance BLOCKED; production writer
promotion NO-GO.** This is a review candidate stacked on H4-A, not a deployment or
session-provisioning implementation. No production mutation was performed.

## Reconstructed state versus prior report

GitHub Draft heads checked through the GitHub connector before development:

| Source | Current verified head |
|---|---|
| application H3 PR #3 | `4cb09fb6243fcc787ae2fe08c17c367fbd06fdf7` |
| application H4-A PR #4 | `0b3c319c96b0da0f921d63e8be6e5b063c66b0fc` |
| infrastructure PR #1 | `33270c20a8fce9aa703c3e18f66d1885d108ec7f` |

HARNESS_BRIDGE_SPEC.md, ARCHITECTURE/CONTRACTS, ADR-010–013, H4-A audit/review,
provisioning plan and current infrastructure rotation/acceptance evidence were
read. The old report's rotation HOLD is superseded: infra #1 records the explicit
A+B authorization and completed scoped H3 rotation. This task does not reauthorize
any rotation/restart. The installed Bridge independently reports H3 revision
4cb09fb with vcs.modified=false; SHA-256 below; actual PID572369. Token expiry
2026-10-26T16:10:27Z was independently read as a nonsecret field and matched the
running process. No token value was printed or changed.

H3 E2E/security and isolated H4-A eleven-tool success are prior committed evidence,
not newly rerun Claude E2E. H4-A is still not the installed writer. Operator reported
successful Claude connector read-only discovery after credential handoff: one
session, expected cwd,16 messages, no mutation. Classify that as **operator-reported
CONNECTOR_READ_ONLY_VERIFIED**, not this task's independent connector observation.

New live observations in this task: the public HTTPS endpoint returned401 without
credentials with TLS verification result0 (no bypass); native test server :4097
GET-only inspection at22:54:33Z independently matched all16 historical messages,
8user/8assistant, original12 and the two later ordered pairs against private
baselines. Every observed turn has exact parent/session/cwd, positive completion
and terminal finish; zero pending permission/unfinished observed turn. Missing
native status was not used as success proof. The original root API :4096 was not
contacted. No old Bridge execution ID/key was queried/replayed after rotation;
those mappings were process-local and are now sealed historical evidence.

## Security findings and implementation

1. **Same-path workspace replacement (high):** canonical path strings alone let
   a new inode inherit an old execution's evidence identity. Capture directory
   FileInfo at acceptance; compare os.SameFile on same-key reads, status/result,
   MCP original-session validation, before Prompt/Abort and after native reads.
   Symlink aliases and non-directory admission are refused. Replacement returns
   FORBIDDEN without content; uncertain guards are retained and terminal records
   remain immutable. These are observed-change checks, not an OS lease/sandbox.
2. **Unjoined/unbounded admission (medium):** manager Close previously did not
   cancel/join in-flight native preflight. Register admission in the manager
   waitgroup under its closed gate, propagate shutdown cancellation, cap native
   admission at4 and total admission/queue time at5s. Reconciliation has its own
   four slots. Canceled/rejected reservations and key fingerprints are cleaned.
   Twenty concurrent fixture sessions verified the active bound and queue budget.
3. **Native filesystem authority (high, unresolved):** effective live OpenCode
   unit has UID995/GID987, ProtectSystem=strict, ProtectHome=yes, PrivateTmp=yes,
   NoNewPrivileges=yes and empty capabilities; no RootDirectory/RootImage,
   PrivateDevices or RestrictNamespaces, shared host network/PID namespaces.
   Earlier independently recorded mount/DAC probes showed readable host public
   files/other project visibility and own runtime credential access without
   reading contents. Those probes were not rerun as native tool operations.
   Bridge hardening was not assumed to protect OpenCode.

[ADR-014](adr/ADR-014-workspace-evidence-and-admission.md) states the exact new
behavior and limits. The earlier H4-B creation/dynamic-grant plan stays design-only
behind this security gate. No DB/broker/frontend/supervisor/Claude Code adapter,
new MCP tool, ACL expansion, native provisioning or automatic retry was added.

## Independent confinement artifact

[tests/confinement](../tests/confinement/README.md) incorporates the earlier local
prototype into reviewable source and repeats real kernel tests here. It is a
separate standard-library-only Go test module, not a launcher to install/expose.
Tests use new synthetic workspaces/state/credentials, non-root UID65534 workers,
private mount/PID/network namespaces, explicit rootfs binds and seccomp. Neither
real credentials/session state nor the real workspace/Docker socket is mounted.
Host files used are only explicit public shell/loader/libc and /dev/null runtime.

Eight permissive controls and42 expected checks for each of two profiles passed.
Positive checks include workspace read/write/rename/native executable/real shell
script and required runtime/cache/temp access. Negative checks cover outside
read/write/execute, absolute/relative/host-path symlinks, other workspace,
credential areas, pathname/abstract Docker-shaped socket routes, proc magic-link
and namespace escalation. Private-network and synthetic shared-network variants
are both exercised without joining the production network for the tests.

The42 include expected **residual demonstrations**, not42 full isolation claims:

* Same-UID own credential remains readable inside an allowed state grant.
* An allowed shell interprets a script from a noexec temp mount.
* A newly added pre-imported hard-link fixture exposes/modifies an outside inode
  through its workspace alias. A mount/path boundary does not establish provenance.
* TCP reaches the synthetic peer in network-compatible mode; egress remains open.

`production_ready=false` and `credential_isolation_complete=false` are asserted.
The review-only systemd candidate is **not equivalent to the tested private-PID
fixture**: on systemd255 it hides proc instead. Actual OpenCode/Bun compatibility,
provider/toolchain dependencies, credential separation and egress are untested.
Static verification of a temporary .service exited1 because the fixed proposed
bootstrap does not exist. No unit was installed/reloaded/started. This failed
check is a deployment blocker, not a passing syntax/startup acceptance claim.

## Actual tests and regression proof

[Exact commands, exit codes and package outputs](H4_B_TEST_RESULTS.json).
[Kernel output](../tests/confinement/evidence/kernel-results.json).

| Check | Actual result |
|---|---|
| gofmt check; go build ./...; go vet ./... | each exit0 |
| go test ./... -count=1 | exit0; adapter, execution, policy, Git, MCP/HTTPS fixtures |
| go test -race ./... -count=1 | exit0, all packages |
| confinement module build/vet/unit | each exit0; ordinary unit mode explicitly skips privileged integration |
| opt-in confinement kernel integration under race | exit0; actual root setup/non-root probes executed |
| standalone kernel JSON runner | exit0;8 baseline,42 per profile |
| git diff --check | exit0 |
| candidate systemd static verification | exit1, missing bootstrap; not production-ready |

Go1.27.2 linux/arm64, kernel6.8.0-111-generic, systemd255.4. The module minimum and
existing dependencies remain unchanged. The race test covers the Go harness,
not arbitrary native tools or every kernel race. Official MCP Go SDK integrations
use fresh loopback HTTP/TLS API fixtures: original seven tool names/input contracts,
all eleven H4-A tools, ACL/Host/Origin/Bearer/TLS denials, pagination, read-only guard,
idempotent concurrent admission, caller cancellation, uncertain submission and
late-completion status/result recovery all passed. No live late-completion prompt
or successful native mutation was used for this evidence.

Regression proof: copied the new execution regression tests to an independent
unchanged0b3c319 worktree and ran
`go test ./internal/execution -run 'Test(ReplacedWorkspaceCannotReconcileOrReplay|TerminalCacheRejectsReplacedWorkspace|ReplacementDuringObservationPreventsTerminalAndAbort)$' -count=1`.
Exit1 reproduced accepted replacements/cached results/abort evidence (nil denial).
The same cases now pass, along with pre-Prompt replacement denial, Close joining,
5s admission budget and20-session concurrency tests. Previous fixture development
had caught missing /dev/null positive-execution failures; the current fixtures
include that device and rerun both positive and negative execution controls.

A separate review binary was built with `go build -buildvcs=false -trimpath -o
<protected-audit-dir>/bridge-candidate ./cmd/bridge`, not installed or launched.
SHA-256: `0d39a2885c7244ed0f8d07d7823d1d15ea61927b48c07953b021cfb65a4ab0f2`.
It intentionally has no embedded VCS revision; this review branch supplies source
provenance. Original production binary SHA-256 remains
`05000a48cf6863673f8b6357437ee74d0f442f17162115327567e3620adafd73`.

## Remaining acceptance gates and next milestone

Full H4-B acceptance is BLOCKED by native credential/tool separation, imported
file/inode policy, egress/socket policy, exact packaged rootfs/bootstrap and real
native-runtime compatibility on independent synthetic state. Stat device/inode
comparison is not a retained file descriptor: inode reuse, mount manipulation,
replacement between checks, same-UID attackers, inherited descriptors and blocked
filesystem syscalls remain limits. Current process-local keys/records are not
durable, no manager-wide execution enumeration exists, external native writers
are not fenced by advisory flock, and history/preflight is not atomic. A restart
is never idempotency recovery; no accepted key may be replayed in a fresh manager.

H4-A promotion assessment: fixture compatibility and prior isolated read-only
acceptance support continued review. They do not authorize replacement of the
public writer or prove native confinement. Recommend a separately approved,
independent native-runtime confinement acceptance before writable promotion or
H4-B provisioning. No deployment/promotion in this PR. Do not change ACLs merely
to make repository discovery or a rootfs test pass.

[Future scoped rollout/rollback](../tests/confinement/ROLLOUT.md) requires separate
native lifecycle authorization. Current production has no rollback action because
it was not changed. Keep current services, credentials, routing and original lock
inodes. A failed future writer change must preserve/fence/reconcile accepted work
and restore only the approved reviewed configuration; never restore old native
history or run a second writer. Local test artifacts can simply be withdrawn.

## Final preservation check

At22:58:41–22:58:42Z, GET-only native reconciliation again matched all16 messages
and original12 with zero pending/unfinished observed work. [Sanitized live
checks](H4_B_LIVE_READ_ONLY.json) record the actual observation. Production Bridge
PID572369/start1369613648, test OpenCode PID3679520/start1357526384, original root
OpenCode PID3489290/start1354613519 and Traefik PID2875781/start126350256 retained
identical executable hashes/start identities. The two service namespace identities
and10 protected credential/configuration/watched-file digests were unchanged.
The dirty shyntr file and untracked watched route/certs were preserved. Full
private digests/correlations remain root-only outside Git. No systemd operation,
native mutation, production credential/configuration/ACL/lock modification,
firewall/routing/DNS/Docker change, application installation or merge occurred.
