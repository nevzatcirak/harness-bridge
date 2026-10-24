# Private TLS backend handoff and connectivity procedure

Coordination: [harness-bridge PR #3](https://github.com/nevzatcirak/harness-bridge/pull/3)
and [ix-deployment PR #1](https://github.com/nevzatcirak/ix-deployment/pull/1).
This is a prepared procedure, NOT observed deployment proof. No Docker service,
firewall, DNS, running bridge, or public Traefik route was changed by this work.
Do not merge either PR.

## Agreed configuration contract

Operator-verified infrastructure (provided 2026-10-24): single-node Swarm,
Traefik v2.11.46, public eth0 `91.99.201.115`, Docker gateway `172.18.0.1/16`,
Traefik gateway-network address `172.18.0.12`, existing OpenCode
`127.0.0.1:4096`. These values were supplied by the operator, not independently
retested during this change. Reinspect before running a probe; container IPs
and PIDs can change.

```sh
BRIDGE_LISTEN=172.18.0.1:8443
BRIDGE_PUBLIC_ORIGIN=https://bridge.nevzatcirak.com
```

Use the existing remote configuration contract for certificate/key, named
principal, <=24-hour token expiry, secret token and exact existing session/root
ACL. Backend certificate SAN must contain `bridge.nevzatcirak.com`. The current
approved target is the non-root test OpenCode at `127.0.0.1:4097`, workspace
`/opt/harness-bridge-test/workspace`; the original root :4096 instance is not an
approved remote target. See [local validation evidence](SYSTEMD_LOCAL_VALIDATION.md).
Neither private/public listener is started by this document.

Local mode still defaults to `127.0.0.1:8080` and permits only the previous
loopback names/addresses. Private binding is an explicit BRIDGE_LISTEN opt-in in
remote TLS mode only. RFC1918 IPv4 or IPv6 ULA literals are supported. Public,
wildcard, DNS, link-local, multicast, mapped IPv6, scoped IPv6 and invalid
host/port configurations fail before listening. The OS must own the exact
configured IP; a missing gateway fails the actual bind with no wildcard fallback.
Certificate load failure similarly prevents service startup. No address discovery
or network/interface configuration is performed by the bridge.

This address policy does NOT establish firewall isolation: other containers or
hosts with a route to the private gateway may connect. They still require valid
TLS Host and a credential for the configured ACL. Review existing host routing,
forwarding/NAT and filtering separately before starting the private backend.
A private address alone is not proof that external reachability is impossible.
Do not add firewall rules in this stage.

## Read-only preflight

Run on the host with Docker inspection and namespace privileges. Inspect only
selected fields; full container/service inspection can expose environment secrets.
Choose exactly the existing Traefik container, not a newly created helper or
Swarm service. Do not pull/install anything into it.

```sh
docker ps --format '{{.ID}} {{.Names}} {{.Image}}'
# Set this manually to the single existing Traefik container ID from above.
TRAEFIK_CONTAINER_ID='<existing-traefik-container-id>'
docker inspect --format '{{.State.Running}} {{.State.Pid}}' "$TRAEFIK_CONTAINER_ID"
docker inspect --format '{{range $name, $net := .NetworkSettings.Networks}}{{$name}} {{$net.IPAddress}} {{$net.Gateway}}{{println}}{{end}}' "$TRAEFIK_CONTAINER_ID"
ip -brief address
ss -ltn '( sport = :8443 or sport = :4096 )'
```

Expect the gateway IP on a Docker interface, no bridge bind on `0.0.0.0`, `::`
or `91.99.201.115`, and OpenCode only on loopback. If the backend is not yet
running, record that and stop. Starting it requires the separately reviewed
private-backend deployment step with secret/certificate provisioning; do not
alter the existing OpenCode server. Do not silently start a substitute backend.

## Probe from the existing Traefik network namespace

Use host tools in the existing container's network namespace: this requires no
container packages, bind mounts, network attachment, service update or public
route. It tests the same container network path; it does NOT prove Traefik's
application-level TLS configuration. Host tools and CA files remain in the host
mount namespace. Stop if required tools/privileges are absent; do not change
container/service configuration as a workaround.

```sh
TRAEFIK_PID=$(docker inspect --format '{{.State.Pid}}' "$TRAEFIK_CONTAINER_ID")
# Confirm running=true and positive PID again immediately before each probe.
# Set to an existing, operator-provisioned public CA PEM bundle (no private key).
BRIDGE_CA_FILE='/operator/secure/backend-ca.pem'
nsenter --target "$TRAEFIK_PID" --net -- ip route get 172.18.0.1
```

Expected route source is the inspected container IP (`172.18.0.12` in the supplied
snapshot). Record actual source/interface without credentials. PID/container
restart invalidates the selection; reinspect instead of guessing. A route alone
does not prove a successful TCP connection.

TLS/TCP and CA/name verification, with no token or tool call:

```sh
timeout 10s nsenter --target "$TRAEFIK_PID" --net -- \
  openssl s_client -connect 172.18.0.1:8443 \
  -servername bridge.nevzatcirak.com \
  -verify_hostname bridge.nevzatcirak.com -verify_return_error \
  -CAfile "$BRIDGE_CA_FILE" -brief </dev/null
```

Require successful TCP connection, TLS >=1.2 and chain/hostname verification OK.
Save sanitized verification/version/exit status only. Do not extract production
private keys or disable verification. A timeout/refusal means stop and diagnose
existing listener/route/filtering; it does not authorize infrastructure changes.

HTTP boundary probes, still no credentials and no native operation:

```sh
nsenter --target "$TRAEFIK_PID" --net -- \
  curl --noproxy '*' --proto '=https' --tlsv1.2 --connect-timeout 3 --max-time 10 \
  --resolve bridge.nevzatcirak.com:8443:172.18.0.1 \
  --cacert "$BRIDGE_CA_FILE" -H 'Host: bridge.nevzatcirak.com' \
  --silent --show-error --output /dev/null --write-out '%{http_code}\n' \
  https://bridge.nevzatcirak.com:8443/mcp
```

Require `401` (curl exits 0 on HTTP errors unless --fail is used). Host is
explicitly set WITHOUT `:8443`: backend bind port differs from public HTTPS :443.
SNI and certificate verification use the real public name; --resolve targets the
private gateway without DNS changes. Proxy environment variables cannot redirect
the probe because --noproxy disables them. Do not use -k or follow redirects.

Repeat the exact curl command with `-H 'Authorization: Bearer invalid-probe'`:
require `401`. Then repeat without Authorization and replace Host with
`-H 'Host: incorrect.invalid'`: require `403` (boundary precedes authentication).
Repeat without Authorization, with the correct Host and
`-H 'Origin: https://incorrect.invalid'`: require `403`.
These fixed invalid headers contain no secret. No valid token is needed for TCP,
TLS or denial verification; do not put the real token on the command line.

Record UTC time, backend address, source IP, TLS version, certificate verification
outcome, HTTP statuses and exit codes. An unexpected status is a failed gate.
These probes do not prove tool discovery, successful authorization, native
execution or ordinary Claude Chat E2E.

## ix-deployment PR #1 next-stage requirements (not applied here)

Future file-provider routing must use only `https://172.18.0.1:8443`, exact
public Host `bridge.nevzatcirak.com` and exact Path `/mcp`, preserve Host, and
reference a dedicated serversTransport with `serverName: bridge.nevzatcirak.com`,
explicit CA trust and `insecureSkipVerify: false`. Do not inherit the insecure
global default reported in PR #1. No Authorization/identity rewriting, plaintext
upstream, retrying mutation middleware or native OpenCode route. Validate the
installed proxy's effective configuration and negative certificate behavior
before public routing; namespace curl does not exercise the Traefik TLS client.

The [official Traefik v2.11 service documentation](https://doc.traefik.io/traefik/v2.11/routing/services/#serverstransport)
describes named transports, serverName, rootCAs and certificate verification;
[passHostHeader](https://doc.traefik.io/traefik/v2.11/routing/services/#pass-host-header)
controls Host forwarding. These settings still need validation in the real
installation. The Caddy example in this repository is an alternative, not the
chosen deployment; do not deploy it alongside the existing Traefik.

After operator-reviewed private connectivity evidence, coordinate routing changes
in ix-deployment PR #1. Public TLS/external isolation checks and the ordinary
Claude Chat same-existing-session two-turn proof remain separate pending gates.
