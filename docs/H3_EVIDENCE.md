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
