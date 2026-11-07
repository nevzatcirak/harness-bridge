# P4-PERM: trusted human approval channel from Claude Chat (feasibility)

Date: 2026-11-07. Status: research only (issue #104). No code, live Bridge or
connector change. All sources were accessed on 2026-11-07; repository and
upstream citations are pinned to the commits named in place.

Evidence levels, as `docs/ROADMAP.md` (P4-PERM) requires them to be kept
apart:
- **spec**: the protocol allows it;
- **SDK**: the Go SDK implements it;
- **client doc**: Claude's documentation says the client does it;
- **observed**: seen with the real Claude Chat client.

Client support is never inferred from the spec alone.

## Verdict

| # | Question | Result | Level reached |
|---|---|---|---|
| 1 | Does Claude Chat (web, desktop, mobile) support **elicitation** for custom remote connectors, and in which modes? | **UNVERIFIED**. Claude's connector docs list tools, prompts and resources as supported and do not mention elicitation; "advanced or draft capabilities" are not supported. Only Claude Code documents elicitation. | client doc: silent |
| 2 | If supported, can the server bind the answer to an **authenticated human decision**? | **NOT SUPPORTED** for form mode by the protocol: the answer is a client-asserted value, with no human identity or signature. URL mode could carry one only through a Bridge-hosted page with its own human authentication, which does not exist. | spec |
| 3 | Can the Bridge verify anything about Claude's generic **tool-call consent**? | **NOT SUPPORTED**. Nothing about client-side confirmation reaches the server; it sees only `tools/call` and the bearer header. Generic consent is not approval authority. | spec + client doc |
| 4 | Does **go-sdk** support elicitation; protocol negotiation risk? | **VERIFIED (SDK)**. v1.8.0 (on `main`, latest release) has `ServerSession.Elicit` for protocol 2025-11-25 and earlier, and MRTR input requests for 2026-07-28. A server must handle both paths. | SDK |
| 5 | OpenCode reply API: identity, allow-once/deny, atomicity, idempotency | **VERIFIED (source)**. Reply is first-wins and at most once per pending request in one process. There is no idempotency key, no caller identity beyond the server credential, no durable record, and the legacy route does not bind the permission to the session. | upstream `148842ef` |
| 6 | **Gate** | **NOT FEASIBLE: the fail-closed fallback stays.** | — |

## 1. Claude Chat as an elicitation client

**What Claude documents for connectors.** The current developer guide,
[Build an MCP server for Claude](https://claude.com/docs/connectors/building.md),
covers the client that "claude.ai, Claude Desktop, Claude mobile, Cowork, and
Claude Code" share:
- **Supported, per the page:** tools, prompts and resources; text and image
  tool results; text and binary resources.
- **"Claude doesn't yet support these MCP features":** resource subscriptions,
  sampling, and "advanced or draft capabilities".
- **Elicitation is not mentioned** in either list.
- **Tool call timeout on claude.ai and Desktop:** "240 seconds per tool call".

The older help-center article
([support.claude.com 11503834](https://support.claude.com/en/articles/11503834-building-custom-connectors-via-remote-mcp-servers))
now points to that guide.

**Claude Code (not Claude Chat).** It documents elicitation explicitly
([code.claude.com/docs/en/mcp](https://code.claude.com/docs/en/mcp), section
"Respond to MCP elicitation requests"):
- form and URL dialogs in the terminal;
- cards at claude.ai/code and in the Desktop app's **Code** tab;
- the capability it declares: `elicitation: {form: {}, url: {}}` on 2026-07-28
  connections, but "the bare `elicitation: {}`, which permits elicitation
  requests in form mode only" on earlier-revision connections "such as to
  claude.ai connectors".

This is Claude Code's behavior as a client. It says nothing about claude.ai
chat conversations.

**MCP 2026-07-28 rollout.** The announcement
([claude.com/blog/bringing-mcp-2026-07-28-to-claude](https://claude.com/blog/bringing-mcp-2026-07-28-to-claude),
2026-07-28) says support "is being rolled out across Claude products". It does
not mention elicitation or input-required results.

**Result: UNVERIFIED.** No official source states that claude.ai chat,
Desktop chat or mobile declares the `elicitation` capability or renders an
elicitation for a custom connector. Modes, limitations and turn-end behavior
are therefore also UNVERIFIED. This is not proof of absence. It can be settled
only by observing the capabilities a real Claude Chat client sends (§6, next
decision (b)).

## 2. Can an elicitation answer prove a human decision?

The protocol itself rules this out for form mode. All quotes below are from the
current revision,
[2026-07-28 elicitation](https://modelcontextprotocol.io/specification/2026-07-28/client/elicitation)
and [MRTR](https://modelcontextprotocol.io/specification/2026-07-28/basic/patterns/mrtr).
The 2025-11-25 text is equivalent
([2025-11-25 elicitation](https://modelcontextprotocol.io/specification/2025-11-25/client/elicitation)).

- **Who answers.**
  - MRTR step 3: "Client gathers the requested information from the user **or
    other sources**, then retries the original request". The answer is whatever
    the client puts in `inputResponses` on its own `tools/call` retry.
  - Nothing carries a human identity, a user-presence proof or a signature.
  - Claude Code even ships an `Elicitation` hook "to auto-respond to
    elicitation requests without showing a dialog". So a client can answer
    without a human.
  - Whether Claude Chat's model can answer instead of the human is
    **UNVERIFIED**. The protocol does not prevent it.
- **Identity.**
  - "Servers **MUST** bind elicitation requests to the client and user
    identity", and "**MUST NOT** rely on client-provided user identification
    without server verification".
  - The only identity the Bridge can verify is the connector's bearer header.
    That is one static, shared credential mapped to one configured principal
    (`docs/CONTRACTS.md`, remote auth; ADR-019). It authenticates the connector
    configuration, not a person.
- **Binding to the exact request.**
  - `requestState` is opaque to the client. The server "**MUST** protect its
    integrity (e.g. HMAC or AEAD)" and "**SHOULD** include" principal, a short
    TTL and a digest of the originating request.
  - This can bind an answer to the exact pending permission. It cannot make the
    answer human: "these measures … do not by themselves guarantee single-use",
    and single use "**MUST**" be enforced server-side.
- **Decline and cancel.**
  - `accept`, `decline` (explicit no) and `cancel` (dismissed, "browser failed
    to load", and so on) are defined.
  - Only `decline` would map to a deny. `cancel` and a missing answer must stay
    "no decision".
- **Timeout and turn end.**
  - MRTR: "Servers **MUST NOT** assume that clients will fulfill the
    `inputRequests` or retry the original request". There is no protocol
    timeout; expiry is whatever TTL the server puts in `requestState`.
  - Legacy server-initiated elicitation (2025-11-25 and earlier) has to be
    answered inside the original tool call, and Claude's tool call limit is 240
    s (§1).
  - What Claude Chat does with an open elicitation when the turn ends is
    **UNVERIFIED**.
- **URL mode.**
  - The human acts in a browser on a server-chosen URL. The server "**MUST**
    verify the identity of the user who opens the URL", typically with the MCP
    authorization server's session.
  - The Bridge has no such authorization server or human session: it uses a
    static header and no OAuth.
  - A server-verifiable human action would need a new Bridge-hosted approval
    page with its own human authentication, for example a passkey (WebAuthn).
    That page is an out-of-band operator channel. Elicitation would only
    deliver its link, and a plain link in a tool result could do the same.

**Result: NOT SUPPORTED** for a server-verifiable human decision through
form-mode elicitation. This holds even if Claude Chat supports it: the answer
is client-asserted and indistinguishable from automated or model-authored
input.

## 3. Generic tool-call consent

**Spec
([2026-07-28 tools](https://modelcontextprotocol.io/specification/2026-07-28/server/tools)):**
- "there **SHOULD** always be a human in the loop with the ability to deny tool
  invocations";
- clients "**SHOULD** … present confirmation prompts".

These are client-side recommendations. The `tools/call` request has no field
that reports whether, how, or by whom it was confirmed.

**Annotations:** "clients **MUST** consider tool annotations to be untrusted
unless they come from trusted servers". They are hints from server to client,
never evidence in the other direction.

**What the Bridge sees** (`internal/mcp`):
- the bearer header;
- the JSON-RPC request;
- model-authored arguments.

An "allow this tool" click in Claude, or "always allow", is invisible to it.
Model text such as "the user approved" is an argument value, which ROADMAP
already excludes as evidence.

**Result: NOT SUPPORTED.** The ROADMAP statement that generic tool consent is
not approval authority is confirmed.

## 4. go-sdk

**Versions:** `go.mod` on `main` (`22264de`) pins
`github.com/modelcontextprotocol/go-sdk v1.8.0`. That is the latest release,
published 2026-09-14
([releases](https://github.com/modelcontextprotocol/go-sdk/releases)).

**Server API** (module source `go-sdk@v1.8.0`):

| Path | Where | What it does |
|---|---|---|
| Legacy, 2025-11-25 and earlier | `mcp/server.go:1729-1775` | `func (ss *ServerSession) Elicit(ctx, *ElicitParams) (*ElicitResult, error)`. A server-initiated request. Refuses when the client's `InitializeParams.Capabilities.Elicitation` is nil, or when the mode was not declared (`form` versus `url`). Validates `accept` content against `RequestedSchema`. |
| Guard on 2026-07-28 | `mcp/server.go:1619-1628` | `assertServerInitiatedRequestAllowed` makes `Elicit` fail with "return an InputRequests map instead (multi round-trip requests, SEP-2322)". |
| MRTR, 2026-07-28 | `mcp/mrtr.go` | Handlers return input requests. `handleMultiRoundTripResult` sets `resultType: input_required` for MRTR clients (`:39-60`). `serverMultiRoundTripMiddleware` (`:124`) and `fulfillServerInputRequests` (`:167`) emulate it for older clients by sending legacy requests. |
| Protocol versions | `mcp/shared.go:45-62` | Latest `2026-07-28`; also 2025-11-25, 2025-06-18, 2025-03-26 and 2024-11-05. |

**Negotiation risk:** the version is chosen per request, and Claude rolled
2026-07-28 out gradually (§1).
- A Bridge feature must use MRTR on 2026-07-28 requests and `Elicit` (or the
  SDK emulation) on older ones.
- It must also check the declared modes on each request, because Claude Code's
  claude.ai-connector path declares form-only on older revisions.
- Which version real Claude Chat negotiates with the Bridge is
  **UNVERIFIED**; the Bridge does not log it today.

**Result: VERIFIED at SDK level.** No SDK blocker.

## 5. OpenCode reply API (v1.19.4, `148842ef`)

**Routes:**

| Route | Status | Where |
|---|---|---|
| `POST /permission/:requestID/reply` | current | `packages/opencode/src/server/routes/instance/httpapi/groups/permission.ts:31-43`, handler `handlers/permission.ts:16-37` |
| `POST /session/:sessionID/permissions/:permissionID` (`permission.respond`) | **deprecated** | `groups/session.ts:101`, `:395-408`, payload `{response}` at `:74-76`, handler `handlers/session.ts:362-378` |

The reply body is `reply: once | always | reject` plus an optional `message`
(`packages/schema/src/v1/permission.ts:38-44`).

**Session binding on the legacy route:** the handler only checks that the
session exists (`requireSession`) and then replies by `permissionID`. Nothing
checks that the request belongs to that session.

**Semantics** (`packages/opencode/src/permission/index.ts`):
- **First wins:** `reply` looks up the pending entry and deletes it before
  resolving it (`:110-114`). A second reply gets `Permission.NotFoundError`,
  which becomes HTTP `PermissionNotFoundError` (`handlers/permission.ts:27-33`).
  This is at most once per pending request, inside one process.
- **No idempotency key.** A retried reply after a lost HTTP response returns
  "not found". So do a request that never existed and a request dropped by
  instance disposal (`:54-60`, which rejects every pending request). Native
  data cannot tell these apart.
  - `GET /permission` lists pending requests only.
  - `permission.replied` is not durable (ADR-018 §1(c)).
  - Lost-response resolution is therefore impossible from native evidence
    alone.
- **Scope:**
  - `always` adds allow rules for the request's `always` patterns, and
    auto-approves other pending requests in the session that now match
    (`:143-166`). That is wider than allow-once.
  - `reject` cascades to every other pending request of the session
    (`:121-139`).
  - Only `once` and `reject` would fit an allow-once/deny relay.
- **Identity:** the server sees only its own Basic Auth credential, which the
  Bridge shares. There is no field for who decided.

**Result: VERIFIED (source).** The native side can take a single allow-once or
deny for an exact `requestID`. It cannot prove who decided, and it cannot
resolve a lost reply without re-issuing it, which the security invariants
forbid.

## 6. Gate verdict and next decision

**Verdict: NOT FEASIBLE, so the fail-closed fallback stays.** Reasons:
- **(1)** Claude Chat elicitation support is UNVERIFIED in official sources.
- **(2)** Even where elicitation exists, a form-mode answer is client-asserted
  and cannot prove a human decision. The Bridge's only verifiable identity is a
  shared static connector credential.
- **(3)** Generic tool consent is invisible to the server.
- **(5)** The native reply API has no lost-response resolution or decider
  identity.

The SDK (4) is not the obstacle.

P5-PERM stays **BLOCKED**; ROADMAP P4-PERM remains open for new evidence.

**Next decision for the maintainer.** One of:
- **(a) Keep the fallback.** No change. Native requests are approved in the
  native OpenCode UI through the trusted operator workflow.
- **(b) Commission a read-only capability probe (R-run).**
  - Scope: record, on a test Bridge, the protocol version and
    `clientCapabilities.elicitation` that real Claude Chat (web, desktop,
    mobile) declares. Optionally show one harmless form-mode elicitation with
    no native effect, to observe rendering, decline, cancel, turn-end and
    timeout.
  - Limit: this would settle item 1 only. It cannot change item 2.
  - Needs a small, separately reviewed logging change.
- **(c) Design an out-of-band approval page as a new ADR.**
  - Shape: a Bridge-hosted HTTPS page with its own human authentication
    (passkey).
  - Binding: an HMAC-protected, short-lived, single-use token bound to
    principal, `requestID`, session, execution, scope and expiry.
  - Choices: `once` or `reject` only.
  - Delivery: by URL elicitation where supported, otherwise as a plain link in
    a tool result.
  - This is the only shape found that gives a server-verifiable human action.
    It is a new security-critical component and needs its own review. Item 5's
    lost-response gap still applies: it must record the attempt before replying
    and must never re-reply.

## Limitations

- No live client was observed. Every Claude Chat statement rests on published
  docs as of 2026-11-07, and silence was recorded as UNVERIFIED.
- Claude docs change without versioned URLs; quotes may age.
- The OpenCode analysis is source reading at `148842ef`, not a live run.
