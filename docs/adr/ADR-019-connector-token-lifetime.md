# ADR-019: Connector token lifetime

Date: 2026-11-06. Status: **ACCEPTED, option (2)** (maintainer, 2026-11-06,
via Planning Desk; issue #98). Option (3), OAuth, remains the long-term option
and needs its own ADR. Option (4) is rejected. Implemented by the
ADR019-IMPL change (`BRIDGE_TOKEN_MAX_LIFETIME`).

Originally proposed 2026-11-06 with no code change.

Numbering: ADR-015 and ADR-016 are referenced in `docs/` without files and are
treated as reserved. ADR-017 exists. ADR-018 is free on `main`, but open issue
#78 (F-7 permission rejection, Worker C) reserves it. This ADR therefore takes
ADR-019.

Preserves ADR-010 (single writer, memory-only execution records) and the remote
boundary in CONTRACTS.md "H3 remote HTTP contract". Amends nothing yet.

## Problem

The Claude Chat custom connector authenticates to the Bridge with one static
bearer token. The Bridge refuses to start in remote mode if that token's expiry
is more than 24 hours away. The maintainer must therefore create a new token,
restart the Bridge and remove and re-add the connector at least once a day.
That breaks unattended operation (for example the R5 supervisor loop) and makes
every rotation a Bridge restart.

## 1. Current mechanism (verified in code, `main` `fb298ed`)

| Behavior | Location |
| --- | --- |
| The token is read once at startup from `BRIDGE_TOKEN` or `BRIDGE_TOKEN_FILE` (private regular file or systemd credential, at most 16 KiB) and must be at least 32 characters. | `cmd/bridge/main.go:20-26`, `cmd/bridge/secret.go:13-38` |
| Any remote setting enables remote mode, which requires TLS, `BRIDGE_PUBLIC_ORIGIN`, `BRIDGE_PRINCIPAL` and an RFC3339 `BRIDGE_TOKEN_EXPIRES`. | `cmd/bridge/ingress.go:31-41` |
| `NewRemote` rejects an expiry that is not in the future or that is more than 24 hours from now. This is a startup check only. | `internal/security/remote.go:21-30` (the cap is line 26) |
| Every request first passes `Remote.Check`: TLS, exact Host, at most one Origin equal to the configured origin, exact path `/mcp`, no query string. Then, if `now >= expires`, it returns `401` with `WWW-Authenticate: Bearer realm="harness-bridge", error="invalid_token"`. | `internal/security/remote.go:32-56` (expiry at lines 45-47), called from `internal/mcp/server.go:192` |
| After the boundary, exactly one `Authorization: Bearer <token>` header must match in constant time; otherwise the response is `401` with `WWW-Authenticate: Bearer realm="harness-bridge"`. | `internal/mcp/server.go:199-207` |
| The audit log records only the principal and the outcome: `remote boundary … boundary_allowed=` and `remote authentication … accepted=`. | `internal/security/remote.go:50`, `:59-61`; `internal/mcp/server.go:202-211` |
| The process handles only SIGINT and SIGTERM. There is no reload of the token or the expiry. | `cmd/bridge/main.go:65` |
| Local (loopback, non-remote) mode has no expiry at all. | `cmd/bridge/ingress.go:31` (remote settings absent) |

**What happens at expiry.** The process keeps running. Every remote request,
including ones that carry the correct token, is rejected with `401
invalid_token` at the boundary, before the token is compared. Expiry does not
interrupt a prompt the native agent has already accepted
(H3_CONNECTOR_GUIDE.md, "Threat model and limits").

**Rotation needs a restart.** The token and expiry are fixed in memory at
startup, so changing either requires a Bridge restart. A restart loses all
in-memory execution, idempotency and parent-ID records (ADR-010). Every
rotation is therefore also an ADR-010 writer restart. That requires the fenced
procedure (fence writers, reconcile native history, never reuse old keys), and
today it happens at least daily.

## 2. What the Claude custom connector supports (official sources)

Accessed 2026-11-06 UTC:
- [Add a connector that isn't in the directory](https://claude.com/docs/connectors/custom/add-unlisted)
  ("add-unlisted");
- [Authentication for connectors](https://claude.com/docs/connectors/building/authentication)
  ("authentication").

Verified from those pages:
- **Three authentication modes** (authentication): OAuth 2.0, where each user
  signs in; a static credential in a request header, entered once by an Owner
  ("This is in beta"); and no authentication.
- **Request headers are a limited beta** (add-unlisted): "Request header
  authentication is in beta and available to a limited set of organizations."
- **How header values are sent** (add-unlisted): Claude "sends it on every
  request", exactly as entered, with no added prefix. The `Authorization` value
  must include `Bearer `. A connector can have up to four headers. On an OAuth
  connection, `Authorization` cannot be configured as a request header.
- **Auth settings cannot be edited** (add-unlisted): "You can't change
  authentication settings after you add a connector. That covers OAuth
  credentials and request headers. To change them, remove the connector and add
  it again with the new details. Members then need to reconnect."
- **One shared credential** (authentication): every member's requests carry the
  same key, so it must not be used to identify the calling user.
- **OAuth registration** (authentication): Claude supports OAuth with Dynamic
  Client Registration (`oauth_dcr`) and with a Client ID Metadata Document
  (`oauth_cimd`) by default. For a custom connector it can also use an OAuth
  client ID that the customer enters.
- **OAuth flow details** (authentication):
  - PKCE S256 on every authorization request;
  - discovery through RFC 9728 protected-resource metadata, started by a `401`;
  - callback `https://claude.ai/api/mcp/auth_callback`.
- **Token refresh** (authentication): Claude "refreshes tokens reactively on a
  `401` response, and proactively up to five minutes before the stored expiry".
  It appends `offline_access` when the authorization server advertises it, to
  obtain a refresh token. Refresh tokens must be rotated for public clients.
- **No machine-to-machine grant** (authentication): "A machine-to-machine
  `client_credentials` grant isn't supported."
- **Egress range** (authentication): Anthropic's outbound traffic originates
  from `160.79.104.0/21`.

UNVERIFIED (not stated in the sources above):
- how claude.ai reacts when a static-header connector receives `401
  invalid_token`, whether it shows an error or starts OAuth discovery;
- any documented maximum lifetime or rotation reminder for request-header
  values;
- whether the operator's current account still has Request headers access. The
  R5 runs used them according to repository evidence; this ADR did not inspect
  the account.

## 3. Options

### (1) Keep the 24 h cap (status quo)

No change. The operator rotates at least daily: new secret, Bridge restart, and
remove and re-add the connector.

### (2) Operator-configurable maximum lifetime with a hard ceiling

Add `BRIDGE_TOKEN_MAX_LIFETIME` (a Go duration). The default stays `24h`, so
existing deployments are unchanged. `NewRemote` would reject an expiry beyond
`now + max lifetime`, and would reject a max lifetime above a compiled-in hard
ceiling (proposed: `720h`, 30 days) or at or below zero. Optional hardening in
the same change:
- raise the minimum token length for lifetimes over 24 h, for example 43
  characters, about 256 bits as base64url;
- log the configured expiry, not the token, once at startup.

### (3) OAuth for the connector

Implement, or put in front of the Bridge, an OAuth 2.1 authorization server and
resource-server validation as Claude requires: protected-resource metadata,
PKCE S256, DCR or CIMD, form-encoded token endpoint, refresh-token rotation,
callback `https://claude.ai/api/mcp/auth_callback`. Access tokens can then be
short-lived (minutes to hours) and Claude refreshes them by itself. Rotation
stops needing a connector re-add or a Bridge restart. There is no
`client_credentials` option, so a human signs in through a consent screen at
least once.

### (4) Rotation without a connector re-add (dual-token overlap)

The Bridge would accept a current and a next token during an overlap window,
which needs hot reload of the token file or a second token setting. **This
cannot avoid the connector re-add**: the connector's request-header value
cannot be edited, only replaced by removing and re-adding the connector
(section 2). An overlap window only lets the operator re-add the connector
before the old token stops working, and only together with a new reload
mechanism, so that the Bridge itself does not restart. The daily manual re-add
remains. As a fix for the stated problem, **(4) is not feasible**; it is at
most a downtime-smoothing addition to (1) or (2).

## 4. Threat analysis

Common invariants that every option keeps:
- the loopback-only native harness (OpenCode on loopback; the Bridge is the
  only remote surface);
- authenticated HTTPS with exact Host and Origin checks;
- one named principal and an immutable session/root ACL (least privilege);
- no token in URLs or logs.

| | (1) 24 h | (2) configurable ≤ ceiling | (3) OAuth | (4) overlap |
| --- | --- | --- | --- | --- |
| Token leak window | ≤ 24 h | ≤ the configured lifetime (≤ 30 d) | access token: minutes to hours; refresh token: rotating, revocable at the authorization server | ≤ lifetime plus overlap; two valid tokens during overlap |
| Immediate revocation | stop the Bridge, or restart with a new token; remove the connector | same as (1); the only way, since there is no denylist | revoke at the authorization server; access tokens die at their short expiry | same as (1), and both tokens must be invalidated |
| Audit | principal and outcome only; restarts recorded by systemd | same, plus the configured expiry logged at startup | can attribute a user and a client ID; needs new audit fields | must record which token matched, without logging it |
| Public exposure | `/mcp` only | `/mcp` only | adds public authorization-server endpoints (`/.well-known/*`, `/authorize`, `/token`, `/register`) with their own attack surface and availability needs | `/mcp` only |
| Restarts and ADR-010 reconciliations | ≥ 1 per day, each losing in-memory execution and idempotency records | one per rotation period, chosen by the operator | none for rotation | depends on reload; without it, same as (1) or (2) |
| Implementation risk | none | small: a parse plus a bound in `NewRemote`; tests for default, ceiling and negative values | large: a security-critical new component or gateway; per-user identity changes the principal model | medium: reload and dual-compare logic in the auth path |

Notes:
- **A longer lifetime is a real cost.** A leaked token under (2) is usable for
  up to the configured lifetime unless the operator stops or restarts the
  Bridge. This needs to be documented as the revocation procedure:
  1. stop `harness-bridge-private`, or restart it with a fresh token;
  2. remove the connector;
  3. reconcile per ADR-010.
- **The existing boundary still limits a leaked token.** It is usable only from
  a client that reaches the HTTPS origin with the exact Host. A firewall
  allowlist for `160.79.104.0/21` (Anthropic egress) narrows this further, but
  that is a deployment control and was not verified here.
- **Daily rotation has its own risk.** (1) makes the operator handle the secret
  and reconcile native state every day. That is error-prone in its own way
  (secret handling mistakes, skipped reconciliation), so the short window is
  not free.

## 5. Recommendation (accepted 2026-11-06: option (2))

**Adopt (2)**:
- `BRIDGE_TOKEN_MAX_LIFETIME`, default `24h` and so unchanged behavior;
- a compiled-in hard ceiling of `720h`;
- a stricter minimum token length above `24h`;
- the configured expiry logged at startup;
- the revocation procedure above, documented in H3_CONNECTOR_GUIDE.md;
- operators advised to choose the shortest lifetime that fits unattended
  operation, for example 7 days.

Keep (3), OAuth, as the long-term option if per-user identity or rotation
without restarts becomes a requirement. It needs its own ADR and security
review. Do not pursue (4) as a fix: the connector cannot change a header
without being removed and re-added, so it cannot remove the manual re-add.

Acceptance note (maintainer, 2026-11-06, via Planning Desk): option (2) is
accepted as recommended above; (3) stays the long-term option with its own ADR;
(4) is rejected. The implementation (issue #98) adds `BRIDGE_TOKEN_MAX_LIFETIME`
with tests for the default, the ceiling, out-of-range rejection and the
unchanged 24 h behavior, plus the CONTRACTS.md and H3_CONNECTOR_GUIDE.md
updates.
