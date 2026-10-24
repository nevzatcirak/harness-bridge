# H3 single-writer and Claude feasibility evidence

Date: 2026-10-24 UTC. Development-only delivery; no binary installed or service
restarted. Application baseline 2eace74884dac133e58fc2444c9070f3dcb116fe;
deployment baseline 58ecd5ac468e22d9849bf44c0c554ebeb52700dc.

Decision: [ADR-010](adr/ADR-010-single-writer.md), option A: one host-local writer,
read-only additional ingress, no forwarding service or distributed infrastructure.
Operator setup/account gates: [connector guide](H3_CONNECTOR_GUIDE.md).

## Exact final checks

All exited zero after the final source change:

```sh
gofmt -w cmd internal
go build ./...
go vet ./...
go test ./... -count=1
go test -race ./... -count=1
git diff --check
```

Final unit package results: cmd/bridge 0.019s; adapters/opencode 0.076s;
execution 0.097s; mcp 1.496s; security 0.012s. Race results: cmd/bridge
1.043s; adapters/opencode 1.308s; execution 2.133s; mcp 3.914s; security
1.052s. Smoke/privatecheck command packages and mcpclient have no test files.
No failed package or race report in the final suite.

During development the first ownership test failed because the fixture directory
was not private; explicitly provisioning mode 0700 corrected the fixture. The
production check remained strict. Targeted and complete suites then passed.
A review also removed fingerprints retained by rejected preflight requests and
added a retry/no-key-leak regression before the final complete suite.

## What was actually tested

| Requirement | Evidence |
| --- | --- |
| Host ownership | TestOwnershipAcrossProcesses uses a real child process/real Linux flock; rejects overlap, releases partial acquisitions, reacquires after owner closes |
| Insecure ownership | TestOwnershipRejectsUnsafeFiles: absent/relative/public directory, symlink directory/file, public file, hard link, FIFO; all rejected |
| Two simultaneous prompts | TestConcurrentPreflightIdempotency controls blocked preflight; TestSDKConcurrentSingleWriter sends 12 simultaneous official SDK calls; exactly one native fixture POST |
| Same-key replay | Same execution/message IDs through pending admission and accepted replay; existing 20-caller test retained |
| Changed payload | Typed IDEMPOTENCY_CONFLICT, including SDK structuredContent and pending admission |
| Native busy/permission | Native busy blocks; pending permission blocks before Prompt; waiting_for_permission monitor and explicit interrupt remain covered |
| Submission timeout | TestSubmissionDeadlineRetainsCorrelationAndGuard injects DeadlineExceeded after the synthetic native accepts; unknown, same IDs, exactly one send, new key blocked |
| Lost transport reply | TestSDKLostNativeReplyNeverResubmits closes a real HTTP connection after the native fixture records the POST; unknown/replay retains one POST |
| Restart limitation | TestRestartLosesIdempotencyAndExecutionRecords explicitly demonstrates mappings lost and duplicate possible in a new synthetic manager; mandates operator reconciliation, not an exactly-once claim |
| Read-only | TestSDKReadOnlyStaging: both mutations FORBIDDEN, native posts=0 and aborts=0; reads still work |
| Same-session two turns | TestSDKLocalIntegration / TestSDKHTTPSIntegration: seed history retained, exactly two fixture POSTs, session/cwd unchanged and correlated parent/result IDs |
| Unauthorized session | SDK reads/send and execution-read/interrupt authorization tests retained; unauthorized sessions denied |
| Cancellation | Existing before/after-admission cancellation, explicit interruption, timeout/shutdown and terminal-proof tests retained |

These execution and continuation cases use deterministic Go runners or an
OpenCode-shaped HTTP fixture. The SDK/TCP/TLS and OS locks are real, but **no
real OpenCode prompt, model run, interruption or Claude conversation was executed
in this task**. Fixtures cannot prove model context recall. Previous real local
two-turn and private read-only activation evidence is historical and does not
prove this new version deployed. This task does not access root OpenCode :4096.

## Installed services and isolation boundary

Read-only status inspection confirmed unchanged PID / zero restart counts:
local Bridge 3690330, private Bridge 3967692, test OpenCode 3679520. Original
OpenCode PID 3489290 and Traefik PID 2875781/start times were retained.
Firewall service/timer active; hb-firewall check passed. Installed local and
private binary SHA-256 remained respectively
15c03c4bcaeea5c40e3536b153a2f72b42463f2532facb1d49d18ee4f05fdf0d and
603e04f76e847344b9898f9958562668a4edc08a063ca568479abb95b3e8e31e.
Unrelated dirty deployment checkout was preserved. No installed unit, token,
listener, DNS, routing, firewall policy/timer or Swarm stack was changed.

Installed old writers do not participate in the new ownership protocol. Before
write access, separately approve the ADR's quiesce/reconcile/migrate procedure;
all mutation clients must use the selected sole writer. Never simply switch the
private unit from read-only while the local writer remains active. Locks do not
exclude native TUI/direct API clients, other hosts or disjoint directory mounts.
Shared UID/non-root execution does not establish complete filesystem confinement.

## Feasibility result and next gate

The current official docs support Streamable HTTP and fixed Authorization request
headers, but header authentication remains limited beta. Actual account plan,
role, Request headers visibility and sharing scope need operator verification.
No OAuth server was implemented. If beta access is absent, obtain approval for
the minimum OAuth resource-server / existing authorization-server design in the
guide. Then separately approve sole-writer service migration and public routing,
verify external negatives and effective TLS-verifying Traefik transport, and
perform the real Claude same-session two-turn acceptance. Both PRs stay unmerged.
