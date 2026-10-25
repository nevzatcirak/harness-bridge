# H3 operator connection guide

Status: independent read-only private HTTPS was activated and verified in deployment
PR #1 commit 58ecd5a. Single-writer source changes and migration requirements are
recorded in [ADR-010](adr/ADR-010-single-writer.md); no new binary was deployed.
Public write/two-turn E2E requires separate migration and public-routing approval. See [preparation evidence](deploy/PRIVATE_HTTPS_PREPARATION.md). Public ingress and ordinary
Claude Chat E2E have NOT been run. PR #3 must remain draft and unmerged.

## Verified official requirements (2026-10-24 UTC)

[Claude custom connector documentation](https://claude.com/docs/connectors/custom/add-unlisted)
requires an HTTPS MCP URL. Fixed Request headers are supported in a limited
organization beta. Choose No sign-in and a Required `Authorization` header with
value `Bearer <credential>`; Claude sends the full value as entered. This is
fixed-credential authentication, not OAuth. Authentication changes require
removing and adding the connector again. Anyone using the connector shares its
credential; this deployment is restricted to one named operator. Confirm beta
availability BEFORE publishing anything.

[Claude builder authentication](https://claude.com/docs/connectors/building/authentication)
and [remote server guidance](https://support.claude.com/en/articles/11503834-building-custom-connectors-via-remote-mcp-servers)
describe OAuth and Streamable HTTP/SSE support. For OAuth, Claude supports
pre-registration, DCR, and published client metadata; its documented callback is
`https://claude.ai/api/mcp/auth_callback`. This bridge implements no OAuth server,
client registration, callback, refresh endpoint or token passthrough. If Request
headers are unavailable, STOP: this implementation cannot connect that account.
A separately reviewed OAuth resource-server implementation/gateway is required;
no gateway interoperability is claimed here.

[MCP authorization](https://modelcontextprotocol.io/specification/2026-07-28/basic/authorization)
defines OAuth resource metadata, authorization server discovery, audience-bound
token validation and OAuth 2.1. Our fixed credential is not a claim of OAuth
conformance. [MCP transports](https://modelcontextprotocol.io/specification/2026-07-28/basic/transports)
describe Streamable HTTP and backward compatibility. The bridge deliberately
retains H2's negotiated 2025-11-25 protocol, not the latest stateless protocol.
[Official Go SDK v1.8.0](https://github.com/modelcontextprotocol/go-sdk/tree/v1.8.0)
provides the server and client used in the HTTPS tests. Actual Claude protocol
negotiation remains an operator acceptance gate.

[Claude network requirements](https://support.claude.com/en/articles/11175166-get-started-with-custom-connectors-using-remote-mcp)
explain that connections originate in Anthropic's cloud. A local browser reaching
loopback is insufficient. Use public DNS and trusted TLS with firewall rules
reviewed by the operator. No public listener or tunnel was started by this task.

## Prepare privately

1. In ordinary claude.ai, Customize > Connectors > Add custom connector, confirm
   that No sign-in and Request headers are available. Team/Enterprise may require
   an owner. Do not save a public URL yet. If unavailable, stop as above.
2. For the current approved test deployment use only the existing non-root
   OpenCode on `127.0.0.1:4097`, workspace `/opt/harness-bridge-test/workspace`.
   The original root server on :4096 is not an approved remote target.
   See [local systemd evidence](deploy/SYSTEMD_LOCAL_VALIDATION.md).
   Select the EXISTING OpenCode native session, read its history, and verify
   its real project directory. Keep test OpenCode on `127.0.0.1:4097`; preserve its
   existing credentials, process, TUI and permission settings. Do not create a
   substitute session. The bridge cannot grant native permission requests.
3. Choose an operator-owned DNS name (example `bridge.example.com`), a publicly
   trusted frontend certificate and a backend certificate for that DNS name.
   The backend may use a private CA if the proxy explicitly trusts it. Restrict
   key and environment-file permissions to the service user (0600); run as an
   unprivileged user with only the selected project access. Never commit secrets.
4. Configure the following environment in the service manager. Replace the two
   marked operator values; do not copy a historical redacted session identifier.

```sh
BRIDGE_READ_ONLY=1
BRIDGE_LISTEN=127.0.0.1:8443
BRIDGE_PUBLIC_ORIGIN=https://bridge.example.com
BRIDGE_TLS_CERT=/operator/secure/backend-fullchain.pem
BRIDGE_TLS_KEY=/operator/secure/backend-key.pem
BRIDGE_PRINCIPAL=operator
BRIDGE_ALLOWED_ROOTS=/opt/harness-bridge-test/workspace
BRIDGE_ALLOWED_SESSIONS=<EXACT_EXISTING_NATIVE_SESSION_ID>
OPENCODE_URL=http://127.0.0.1:4097
```

Set `BRIDGE_TOKEN` to a fresh cryptographically random secret (e.g. 32 random
bytes encoded as hex), and `BRIDGE_TOKEN_EXPIRES` to an RFC3339 UTC timestamp
within the next 24 hours. Supply existing `OPENCODE_SERVER_USERNAME` and
`OPENCODE_SERVER_PASSWORD` securely. Do not print secrets, paste them in chat,
put them in URLs/command arguments or enable shell tracing. Token rotation or
revocation requires restarting the bridge (a restart loses in-memory execution
and idempotency records; reconcile native history before retrying mutations).

```sh
go build -o /tmp/harness-bridge ./cmd/bridge
/tmp/harness-bridge
```

Remote mode requires TLS, the named principal and expiry. The backend defaults
to loopback; an explicitly configured RFC1918/ULA literal is allowed only in
remote TLS mode. See the [Traefik private-backend handoff](deploy/TRAEFIK_PRIVATE_BACKEND.md)
for the selected `172.18.0.1:8443` target and container connectivity procedure. Legacy local H1/H2 configuration is retained; never forward its
plaintext listener publicly. Do not use a tunnel that skips the remote boundary.

## Security review, then operator-controlled publication

For the existing Swarm installation use the [Traefik handoff](deploy/TRAEFIK_PRIVATE_BACKEND.md).
The Caddy configuration is an alternative example, not a deployment instruction
for that installation. Review [the example proxy configuration](deploy/Caddyfile.example) and the threat
model below before enabling it. The example has NOT been deployed/tested with
Caddy. Install/configure Caddy separately; verify syntax against the installed
version. Proxy only `/mcp` to the loopback TLS bridge, preserve the exact public
Host, verify backend certificate name/chain, and never proxy port 4096. No
forwarded header is trusted for identity, host or TLS. Public DNS resolves to
the proxy; only approved HTTPS ingress is exposed. Verify externally that
unauthenticated `/mcp` returns 401 with a Bearer challenge, wrong token returns
401, unexpected Host/Origin is denied and other paths cannot reach OpenCode.
Do not enable access logging of credentials, headers or request/response bodies.

Publication requires explicit operator action/security review per PR #3. The
repository contains configuration only; it does not provision DNS, certificates,
firewall rules, hosting or a public tunnel.

## Ordinary Claude Chat acceptance procedure

1. Add `https://bridge.example.com/mcp` in Customize > Connectors. Choose
   No sign-in, Required Request header `Authorization`, value `Bearer ` followed
   by the secret. Keep Streamable HTTP; do not add `/sse` or OAuth credentials.
2. Enable the connector in a normal chat via + > Connectors. Confirm exactly
   seven tools. Restrict tool permissions; approve prompts/interrupts deliberately.
3. Ask Claude to `list_sessions`, select the exact existing session, `get_session`
   and `read_session_history`. Compare directory and prior history privately.
4. Send harmless prompt #1 with a unique context marker and ask the existing
   agent to report its cwd. Record execution ID; poll status to a correlated
   terminal result, inspect `get_execution_result` and the existing terminal.
   Resolve permission waits only in the native operator workflow.
5. Send prompt #2 to the SAME session, asking it to recall the first marker
   without repeating it, and report cwd. Verify assistant parent IDs, both native
   prompt IDs, seed history retention, identical native session and directory.
   HTTP 204, idle, or connector discovery alone is insufficient.
6. Store redacted evidence: UTC timestamps, tool names, state, comparison booleans,
   message-count changes and terminal verification. Exclude secret/header values,
   full session/execution IDs, message contents and private paths. Update H3
   evidence only after observing the real conversation. Leave draft open until
   acceptance and human review; do not merge.

401: check exact Bearer prefix, token expiry and rotation. 403: check exact Host,
TLS to backend and same-origin Origin. TLS errors: verify both certificate chains
and DNS names; never disable verification. OAuth screen/no Request headers:
stop and obtain beta availability or implement a reviewed OAuth path. Native
permission waits are expected states, not authentication failures.

## Threat model and limits

One credential maps to one configured operator and one immutable session/root
ACL. This does not identify different people sharing a credential; sharing is
unsupported and requires explicit review plus per-user authorization. Tokens
are reusable until expiry/restart; protect them as privileged credentials.
TLS >=1.2, 16 KiB header limit, timeouts, a 1 MiB JSON body limit and exact
Host/Origin checks bound requests. Forwarded headers cannot bypass TLS or Host
validation. In remote mode the explicit exact Host boundary replaces the SDK's
loopback-only Host check (which would reject legitimate proxy traffic); local
mode retains the SDK default. Go's cross-origin guard remains active.

Symlink-resolved roots and native session ACL are rechecked for every operation,
including execution reads/interrupts. Native filesystem/process compromise and
concurrent symlink replacement are outside this boundary; isolate the service OS
user. Native permission controls, uncertainty, busy-session and duplicate guards
remain unchanged. Agent output can contain prompt injection; inspect instructions
and grant no additional native permissions based solely on tool output. Audit
records contain configured identity and outcomes, not payloads or native IDs.
No global rate limiter or durable execution store is added; deployment firewall
rate controls and operator reconciliation remain necessary. Expiry blocks new
requests; it does not interrupt a prompt already accepted by the native agent.

## Current account feasibility and exact selected-host setup (2026-10-24)

Rechecked the [official support article](https://support.claude.com/en/articles/11175166-get-started-with-custom-connectors-using-remote-mcp):
custom connectors are listed for Free/Pro/Max/Team/Enterprise (Free: one).
Organization creation rights differ from member connection rights. This does
not establish this operator's actual account/organization entitlement. Connections
come from Anthropic infrastructure, not the user's browser or desktop network;
the current private backend alone is unreachable to ordinary Claude Chat.

The [current add-connector instructions](https://claude.com/docs/connectors/custom/add-unlisted)
explicitly limit Request headers to a beta subset of organizations. Inspect the
actual account dialog before making public infrastructure changes. Layouts vary.
For an entitled account, after public-routing approval and external verification:

1. Individual account: Customize > Connectors > Add custom connector. For an
   organization, an authorized Owner creates it in Organization settings >
   Connectors; do not expose this privileged shared credential to other members.
2. Name: Harness Bridge. URL: `https://bridge.nevzatcirak.com/mcp`. Transport:
   Streamable HTTP, not an invented `/sse` endpoint.
3. Authentication: No sign-in. Request headers: standard `Authorization`,
   Required, value `Bearer <fresh operator-provided secret>` including the space.
   Enter only in the credential UI; never paste the actual secret in chat,
   URLs, screenshots, Git or process arguments. Leave OAuth client fields unset.
4. If Request headers are absent, STOP; changing the server to unauthenticated
   is forbidden. Record plan/role and visible options without credentials and
   request beta access or decide the OAuth alternative below.
5. Save and connect; enable in a normal chat with + > Connectors. First verify
   discovery/metadata/history on the approved session while still read-only.
   Keep write tools blocked in Claude until the single-writer migration is
   separately approved. Read-only policy remains enforced server-side.
6. After approved migration, perform the earlier two-turn procedure on native
   session `ses_e9329c0b4ffebOlm7GRyKxbMQ5`, unchanged workspace
   `/opt/harness-bridge-test/workspace`. Use a new key for each intended turn;
   replay only the identical key/payload after transport loss in the SAME writer
   lifetime. Preserve execution/message IDs and inspect correlated terminal proof.
   Do not blindly resubmit after restart. Permission approval stays native.

Fixed header values are sent verbatim on every request; they are not exchanged
or refreshed via OAuth. Connector authentication settings require removal and
re-addition to change, followed by reconnecting members. Coordinate Bridge token
rotation/expiry with that UI operation and native-state reconciliation; no
uninterrupted write-token rotation is implemented. Current staging token expires
2026-10-25T16:25:33Z; never reuse it for a future write activation. No setting or
conversation in the operator's actual account was inspected in this task.

## Minimum OAuth alternative — proposed, NOT approved or implemented

[Anthropic authentication requirements](https://claude.com/docs/connectors/building/authentication)
cover discovery, PKCE S256, DCR/CIMD or a preregistered client, hosted callback
`https://claude.ai/api/mcp/auth_callback`, token refresh and form-encoded exchange.
Machine-to-machine client_credentials is unsupported. Request-header beta remains
organization-scoped; a shared token does not establish per-user identity.

If the operator lacks header access, obtain approval for an OAuth resource-server
boundary using an existing standards-compliant authorization server, rather than
writing an authorization server in Bridge. The design must specify protected
resource metadata, 401 discovery challenge, authorization-server metadata,
exact resource URL, audience/issuer/expiry/scope validation and per-user ACL,
authorization-code + S256, chosen client registration and consent, callback,
refresh/revocation/rotation and credential storage. No existing identity service
or provider is assumed usable, and none is modified. OAuth access tokens must not
be passed through as OpenCode Basic Auth credentials. The native credential
remains private and independently authorized. See the
[MCP authorization specification](https://modelcontextprotocol.io/specification/2025-11-25/basic/authorization).

The existing fixed Bearer path is the preferred one-operator MVP only if account
availability and shared-credential access restrictions are verified. Public TLS,
effective Traefik backend CA/ServersTransport, external negative tests, single
writer migration and a real Claude conversation remain separate acceptance gates.
No Claude E2E success is inferred from SDK or unit tests.

## Final activation handoff

Use [H3_CLAUDE_ACCEPTANCE.md](H3_CLAUDE_ACCEPTANCE.md) for the single actual-chat
acceptance script and account/auth-detection checks, and deployment PR #1's
`harness-bridge/FINAL_CONNECTOR_RUNBOOK.md` for gated migration/publication
commands. These are preparation only; do not execute historical first-install
commands against the already running private service.
