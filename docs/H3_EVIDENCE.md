# H3 implementation and evidence — 2026-10-24 UTC

Status: local TLS protocol feasibility PASS; public ingress NOT DEPLOYED;
ordinary Claude Chat E2E NOT RUN. PR #3 stays draft, open and unmerged.

Official requirements were checked before implementation. Sources, protocol
version distinction, limited Request headers beta availability and precise
operator steps are in [the connection guide](H3_CONNECTOR_GUIDE.md). The supported
path is one named operator using a fixed expiring bearer credential; this is not
OAuth. If that account lacks Request headers, the Claude connection is blocked
pending beta access or a separately implemented/reviewed OAuth solution.

## Observed behavior

`TestSDKHTTPSIntegration` uses real loopback TCP TLS with certificate verification
(the test client trusts only the test server certificate) and the official Go SDK
v1.8.0 client/server, with public Host `bridge.example`. It traverses the actual
remote boundary, auth, SDK, policy, execution manager and native adapter. The
OpenCode API is a deterministic HTTP fixture, not a live agent. The test verifies
2025-11-25 initialize, seven strict-schema tools, seed history, two correlated
prompts to the same native session, cwd, results and idempotency/conflicting key
rejection. No TLS verification or policy bypass is used. No public network
remote deployment, Caddy process, live OpenCode mutation or Claude chat was run.

`TestHTTPSAuthenticationDenial` sends actual HTTPS requests for missing/invalid/
expired/duplicate authorization (401), wrong Host/Origin (403) and oversized
body (413). Challenges are asserted for 401. Boundary tests also reject plaintext,
forwarded-header TLS spoofing, query credentials, duplicate Origin and invalid
configuration. Existing H1/H2 tests continue covering cross-session/root denial,
execution read/interrupt ACL, permission waits, busy/conflicting runs, uncertain
submission, cancellation, timeout and interrupted results. Adapter and execution
manager implementation and seven tool schemas are unchanged.

The strict public Host boundary replaces the SDK's localhost-only Host check
ONLY in remote mode. This is required when a loopback proxy backend receives a
public Host; the request must already pass TLS, exact Host/Origin and auth.
Forwarded headers cannot enable this exception. Legacy local mode retains SDK
DNS rebinding protection. Startup rejects partial remote configuration, missing
TLS keys, invalid principal and expired/>24-hour credential lifetime. Native
OpenCode remains loopback-only; no permission endpoint or approval bypass added.
Audit removes native session/execution IDs and records identity/outcome without
headers, contents or secrets.

## Required checks

Environment: `go version go1.27.2 linux/arm64`, SDK v1.8.0 (no module changes).
Final command sequence:

```sh
gofmt -w cmd internal
go build ./...
go test ./...
go vet ./...
go test -race ./...
git diff --check
```

Every command exited 0. gofmt/build/vet/diff check were silent. Final ordinary
test output:

```text
?   github.com/nevzatcirak/harness-bridge/cmd/bridge [no test files]
?   github.com/nevzatcirak/harness-bridge/cmd/mcpsmoke [no test files]
?   github.com/nevzatcirak/harness-bridge/cmd/smoke [no test files]
ok  github.com/nevzatcirak/harness-bridge/internal/adapters/opencode (cached)
ok  github.com/nevzatcirak/harness-bridge/internal/execution (cached)
ok  github.com/nevzatcirak/harness-bridge/internal/mcp 1.372s
?   github.com/nevzatcirak/harness-bridge/internal/mcpclient [no test files]
ok  github.com/nevzatcirak/harness-bridge/internal/security (cached)
```

Race output is recorded below. Whitespace above is normalized from Go's tabs.
An initial sandbox attempt failed writing `/root/.cache/go-build`; reran with
approved cache access. No failed check was concealed. `govulncheck` is not
installed and was not run; no dependency-vulnerability scan claim is made.
Targeted uncached HTTPS checks also exited 0:

```sh
go test ./internal/mcp -run 'TestSDKHTTPS|TestHTTPS' -count=1 -v
```

```text
--- PASS: TestSDKHTTPSIntegration (0.09s)
--- PASS: TestHTTPSAuthenticationDenial (0.60s)
    --- PASS: TestHTTPSAuthenticationDenial/missing (0.01s)
    --- PASS: TestHTTPSAuthenticationDenial/invalid (0.01s)
    --- PASS: TestHTTPSAuthenticationDenial/expired (0.04s)
    --- PASS: TestHTTPSAuthenticationDenial/duplicate (0.01s)
    --- PASS: TestHTTPSAuthenticationDenial/origin (0.01s)
    --- PASS: TestHTTPSAuthenticationDenial/host (0.01s)
    --- PASS: TestHTTPSAuthenticationDenial/oversized (0.52s)
PASS
ok github.com/nevzatcirak/harness-bridge/internal/mcp 0.707s
```

Routine identity/outcome audit lines omitted from targeted output.

## Acceptance gates

- [x] Current official auth/transport verified, account beta limitation identified.
- [x] Local TLS boundary and security tests; strict native ACL/roots preserved.
- [x] Official SDK over HTTPS, two-turn fixture continuation and denial tests.
- [x] Manual ordinary Claude Chat setup and security review instructions.
- [ ] Operator-reviewed public ingress with trusted TLS and external denial checks.
- [ ] Operator account Request headers availability confirmed.
- [ ] Actual ordinary Claude Chat to existing OpenCode two-turn context/cwd proof.

Do not mark H3 complete or convert the PR to ready based on fixture tests.
Follow the guide; capture real redacted chat/terminal evidence before acceptance.

Final race output (exit 0):

```text
?   github.com/nevzatcirak/harness-bridge/cmd/bridge [no test files]
?   github.com/nevzatcirak/harness-bridge/cmd/mcpsmoke [no test files]
?   github.com/nevzatcirak/harness-bridge/cmd/smoke [no test files]
ok  github.com/nevzatcirak/harness-bridge/internal/adapters/opencode (cached)
ok  github.com/nevzatcirak/harness-bridge/internal/execution (cached)
ok  github.com/nevzatcirak/harness-bridge/internal/mcp 3.359s
?   github.com/nevzatcirak/harness-bridge/internal/mcpclient [no test files]
ok  github.com/nevzatcirak/harness-bridge/internal/security (cached)
```

## Private-interface binding follow-up — 2026-10-24 UTC

Continued on `feat/h3-secure-claude-remote-connector` for the operator-supplied
Traefik/Swarm topology. Read ix-deployment PR #1 and its deployment plan before
implementation. [Coordinated private-backend procedure](deploy/TRAEFIK_PRIVATE_BACKEND.md)
defines `172.18.0.1:8443`, public Host `bridge.nevzatcirak.com`, verified backend
TLS and the pending dedicated Traefik transport requirement. The PR reports an
insecure global transport; this must not be inherited by future bridge routing.
No changes to either deployment runtime or infrastructure were made.

`security.ValidateListen` preserves local mode's loopback names/addresses and
permits an explicitly configured RFC1918 IPv4/ULA literal only with validated
remote TLS mode. It rejects the supplied public `91.99.201.115`, other public
IPs, wildcard IPv4/IPv6, DNS names, link-local, multicast, CGNAT, mapped/scoped
IPv6 and invalid host/port forms. Configuration is resolved before listening;
missing/partial TLS or remote identity settings never fall back to HTTP.
Actual address ownership is checked by OS bind, not guessed from interface names.
The implementation does not modify interfaces or claim private IPs are isolated
from routed networks. OpenCode loopback restriction, exact public Host/Origin,
bearer authentication, session ACL, roots and native permission controls remain.

New tests: `TestListenPolicy`, `TestIngressConfiguration` (including incomplete
TLS configuration and public binds in fully configured TLS mode), and
`TestHTTPSListenerRejectsPlaintextAndUntrustedCertificate`. The latter uses a
real TLS socket: plaintext gets 400 before dispatch and an untrusted client
cannot connect. Existing `TestHTTPSAuthenticationDenial` checks wrong Host,
unauthorized/expired/duplicate credentials and other boundary denials; the full
suite also retains TLS SDK continuation and all H1/H2 tests.

Checks rerun on Go 1.27.2 linux/arm64, SDK v1.8.0 unchanged:

```sh
gofmt -w cmd internal
go build ./...
go test ./...
go vet ./...
go test -race ./...
git diff --check
```

All exited 0. Formatting, build, vet and whitespace checks were silent.
Exact test output (tabs normalized):

```text
ok  github.com/nevzatcirak/harness-bridge/cmd/bridge 0.014s
?   github.com/nevzatcirak/harness-bridge/cmd/mcpsmoke [no test files]
?   github.com/nevzatcirak/harness-bridge/cmd/smoke [no test files]
ok  github.com/nevzatcirak/harness-bridge/internal/adapters/opencode (cached)
ok  github.com/nevzatcirak/harness-bridge/internal/execution (cached)
ok  github.com/nevzatcirak/harness-bridge/internal/mcp 1.383s
?   github.com/nevzatcirak/harness-bridge/internal/mcpclient [no test files]
ok  github.com/nevzatcirak/harness-bridge/internal/security 0.011s
```

Exact race output (tabs normalized):

```text
ok  github.com/nevzatcirak/harness-bridge/cmd/bridge 1.029s
?   github.com/nevzatcirak/harness-bridge/cmd/mcpsmoke [no test files]
?   github.com/nevzatcirak/harness-bridge/cmd/smoke [no test files]
ok  github.com/nevzatcirak/harness-bridge/internal/adapters/opencode (cached)
ok  github.com/nevzatcirak/harness-bridge/internal/execution (cached)
ok  github.com/nevzatcirak/harness-bridge/internal/mcp 3.405s
?   github.com/nevzatcirak/harness-bridge/internal/mcpclient [no test files]
ok  github.com/nevzatcirak/harness-bridge/internal/security 1.029s
```

Limitations: the real `172.18.0.1` bind, Traefik-container TCP/TLS probes,
certificate provisioning/validation in Traefik, external isolation checks,
public routing and ordinary Claude Chat live two-turn acceptance remain NOT RUN.
The prepared procedure uses host tools inside the existing container's network
namespace without creating containers or altering services; it is not evidence
that the runtime route works. No firewall/DNS/Swarm/public routing changes, no
OpenCode restart or permission bypass, and neither PR merged. Original pending
Claude account auth availability and live E2E gates remain open.


## Non-root local systemd integration — 2026-10-24 UTC

[Detailed actual evidence](deploy/SYSTEMD_LOCAL_VALIDATION.md): fixed systemd
255's ACL-mask 0440 credential rejection without accepting arbitrary readable
files (implementation `2c97334`); full required checks and genuine systemd fixture
test passed before installing the tested binary. Bridge test service is healthy
on loopback :8080, targets only non-root test OpenCode :4097, and enforces the
single supplied test-session ACL/root. Missing/wrong token denial and SDK
initialize/discovery/metadata/history plus unauthorized-session denials passed.
Two real prompts on the same existing test session completed with marker recall,
parent/cwd correlation and history growth 0→4; no tools used. Empty initial
history means pre-existing nonempty-history retention was not exercised.
Original root OpenCode :4096 PID/start-time unchanged; no native permissions,
workspace ACLs, deployment services, public routes, DNS or firewall altered.
Filesystem read isolation remains unproven and explicitly false for readable
workspace README.md; see detailed ACL evidence. Ordinary Claude Chat E2E and
public/private deployment gates remain pending, and both PRs remain unmerged.
