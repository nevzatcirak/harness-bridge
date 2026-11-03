# H3 private HTTPS preparation — 2026-10-24 UTC

Status: implementation/templates tested; **no new backend or public listener
activated**. Preserve the successful local MCP/native proof. The coordinated
[deployment runbook](https://github.com/nevzatcirak/ix-deployment/blob/feat/harness-bridge-traefik-plan/harness-bridge/PRIVATE_HTTPS_RUNBOOK.md)
contains exact fetch/pull, separate service/artifact, verification, approval gates
and rollback. [Deployment evidence](https://github.com/nevzatcirak/ix-deployment/blob/feat/harness-bridge-traefik-plan/harness-bridge/PRIVATE_HTTPS_EVIDENCE.md)
records certificate, network, Traefik and workflow findings.

## Source changes

Optional `BRIDGE_READ_ONLY=1` rejects send_prompt and interrupt_execution with
FORBIDDEN before any native mutation. Default/0 retain H1/H2 write behavior;
invalid settings fail configuration. The seven tools, native permissions,
session/root policy, exact Host/Origin, expiring Bearer auth and private-bind
restrictions remain. New staged unit uses readonly to avoid dual writers while
the verified loopback Bridge continues. Independent processes do NOT share
execution/idempotency records; write enablement needs an ownership/concurrency
review, not simply changing the flag.

`mcpclient.ConnectPrivateTLS` uses a literal private/loopback network destination,
explicit CA trust, verified public DNS certificate name, exact public HTTP Host,
TLS >=1.2, no proxy/redirect forwarding and official SDK 2025-11-25 negotiation.
`cmd/privatecheck` reads its token from stdin only, checks seven tools, exact
session/workspace, history and unauthorized reads, and submits no mutation.
It prints counts/status only. Source `cmd/mcpsmoke` remains available for the
existing loopback proof. Neither new binary was installed; only /tmp staging
builds were produced. Existing /usr/local/bin/harness-bridge was not replaced.

## Checks and exact scope

Go 1.27.2 linux/arm64, existing SDK v1.8.0. All commands exited 0:

```sh
gofmt -w cmd internal
go build ./...
go vet ./...
go test ./...
go test -race ./...
git diff --check
go build -o /tmp/harness-bridge-private-staged ./cmd/bridge
go build -o /tmp/hb-privatecheck-staged ./cmd/privatecheck
```

Formatting/build/vet/diff check silent. Final ordinary output (tabs normalized):

```text
ok github.com/nevzatcirak/harness-bridge/cmd/bridge (cached)
?  github.com/nevzatcirak/harness-bridge/cmd/mcpsmoke [no test files]
?  github.com/nevzatcirak/harness-bridge/cmd/privatecheck [no test files]
?  github.com/nevzatcirak/harness-bridge/cmd/smoke [no test files]
ok github.com/nevzatcirak/harness-bridge/internal/adapters/opencode (cached)
ok github.com/nevzatcirak/harness-bridge/internal/execution (cached)
ok github.com/nevzatcirak/harness-bridge/internal/mcp 1.593s
?  github.com/nevzatcirak/harness-bridge/internal/mcpclient [no test files]
ok github.com/nevzatcirak/harness-bridge/internal/security (cached)
```

Final race output had the same package results; internal/mcp was `ok 3.605s`,
other tested packages cached; no-test packages as above. New and expanded tests:
TestReadOnlyConfiguration, TestSDKReadOnlyStaging, TestPrivateSDKTLSFailures and
TestSDKHTTPSIntegration. The last exercises actual SDK/TLS tools and two-turn
fixture continuation through the new private TLS client. Negative cases cover
wrong certificate name, invalid CA/token, plaintext and a public backend address;
readonly denies both mutation tools with zero native prompt/abort requests while
preserving reads and the seven tools. Existing security/lifecycle tests passed.
These are synthetic backend tests, not new public/private deployment claims.

New systemd template passed systemd-analyze verify using a temporary copy with
ONLY ExecStart substituted to the compiled staging binary. The literal template
initially failed verification because /usr/local/libexec/harness-bridge-private
is deliberately not installed. No service started, no credential/approval file
created, and no systemd daemon reload performed in this preparation.

Deployment's standard-Go validator ran the existing Traefik v2.11.46 binary in
an isolated, loopback-only network namespace, actual File Provider directory
watch and synthetic trusted frontend/backend certificates. It passed parsing,
Host/Bearer forwarding, 401/403/exact-path checks, wrong-SAN and untrusted-CA
denials despite simulated global insecureSkipVerify=true, and watched-route
removal rollback to 404. Real production default, frontend ACME, private endpoint
and native service were not used/changed by that test. See deployment evidence
for commands/results and expected synthetic TLS handshake errors.

## Actual local runtime preservation

An actual read-only run of the existing systemd local services exited 0:

```text
native idle preflight verified; no pending permissions or unfinished assistant
existing session selected; native ID and directory redacted; history before prompts: 4 messages
missing and incorrect MCP bearer tokens rejected with HTTP 401
official SDK initialize, seven tools, exact-session discovery, metadata/history and unauthorized session denials verified
PASS: read-only local MCP authentication and session policy checks; no prompt submitted
```

Bridge PID 3690330 and test OpenCode PID 3679520 remained active/running,
NRestarts=0; loopback :8080 and :4097 unchanged. Root OpenCode PID/start ticks
3489290/1354613519 and loopback :4096 unchanged. Installed local binary hash
still `15c03c4bcaeea5c40e3536b153a2f72b42463f2532facb1d49d18ee4f05fdf0d`.
No :8443 listener. No OpenCode/native permission/workspace ACL changes.

## Reviewed blockers before real activation

Provisioned backend certificate chain/name/key match and service read access
verified; server certificate expires 2027-01-22 06:56:06 UTC, public CA expires
2027-10-24 06:56:04 UTC. Key remains outside Git and is loaded as a credential in
the new unit. Existing Bridge UID can already read the source key via group mode;
this is not UID isolation. Renewals require explicit certificate/CA review and
restart of only the private service; no renewal/restart automation was added.

Host INPUT ACCEPT, no 8443 source restriction, empty DOCKER-USER; no cloud/routed
isolation proof. The real private listener is therefore **blocked** pending a
separate reviewed host-input policy and operator approval A. The unit requires
an approval marker and has no boot target. Public route remains outside watched
config until actual backend TLS/negative-auth/read-only SDK tests pass AND
operator approval B. No actual private TLS/positive SDK check was run here.

The dirty live deployment main checkout was preserved; a separate worktree holds
PR #1 changes. The existing PR-triggered deployment workflow would redeploy
unrelated Portainer stacks; a minimal job guard now restricts deployment to main
push only before PR #1 is pushed. Main-push behavior is unchanged.

Non-root execution is not complete filesystem confinement: readable workspace
files and OS-permitted native reads remain possible. Session/root ACLs and native
permissions remain in force but are not an OS sandbox. Claude account Request
headers beta/OAuth limitations, multi-user access, public write enablement and
ordinary Claude Chat two-turn E2E remain unverified/pending. No automatic public
routing, DNS/firewall edits, Swarm redeployment or PR merge.
