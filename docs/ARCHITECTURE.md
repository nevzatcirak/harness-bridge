# Harness Bridge — Architecture Baseline v0.1
Date: 2026-10-23

## Source of truth
See `../HARNESS_BRIDGE_SPEC.md` for normative requirements. This document records implementation decisions.

## Decision register
- ADR-001: Go modular monolith; no DB, queue, browser frontend or autonomous supervisor in MVP.
- ADR-002: OpenCode REST on loopback, existing server only. Never start a new OpenCode process implicitly.
- ADR-003: Distinct session ID (`opencode:<native-id>`) and per-prompt execution ID.
- ADR-004: Async prompt submission, polling-based status v0, optional event stream after integration verification.
- ADR-005: Fail closed on uncertain prompt submission. Never retry a possibly delivered prompt automatically.
- ADR-006: User-facing remote MCP requires authenticated HTTPS and per-session authorization. Loopback dev API is *not* public deployment ready.
- ADR-007 (H2): Official `github.com/modelcontextprotocol/go-sdk` v1.8.0 replaces the bootstrap. Typed `mcp.AddTool` handlers and `github.com/google/jsonschema-go` v0.4.3 provide schema inference/validation; module checksums are pinned. Go minimum is 1.25, required by the SDK.
- ADR-008 (H2): Authenticated loopback Streamable HTTP, stateful MCP 2025-11-25, JSON responses. Restrict `SupportedProtocolVersions` explicitly so SDK clients exercise initialize rather than the newer stateless discovery protocol. MCP sessions expire after one minute of inactivity; they are independent of native sessions and detached execution lifetimes. Clients may reconnect without resubmitting prompts.
- ADR-009 (H2): Keep H1 single-principal bearer authentication outside the SDK transport for every HTTP method, including initialize, GET, DELETE and cancellation notifications. Keep the 1 MiB / single JSON value HTTP body boundary. SDK owns all MCP decoding, negotiation, dispatch and cancellation. Enable Go cross-origin protection and retain SDK localhost/DNS rebinding protection. No custom MCP wire implementation remains.

## Functional requirements
FR01 session discovery; FR02 metadata; FR03 history; FR04 same-session prompt; FR05 execution correlation; FR06 status; FR07 result; FR08 interruption; FR09 duplicate suppression; FR10 busy guard; FR11 Claude connector; FR12 verified two-turn continuity.

## Non-functional requirements
Local-first, least privilege, authenticated remote access, per-session ACL, allowed project roots, safe secret handling, zero trust in repo/tool text, bounded requests, timeouts, concurrency safety, testability, minimal dependencies, graceful shutdown, audit.

## Components
Claude Chat -> authenticated ingress -> Go MCP server -> auth/policy -> session manager/registry -> execution manager -> OpenCode HTTP adapter -> *already running* OpenCode server and session.

## Lifecycles
Session state: idle / running / waiting_for_permission / unknown. An execution ending does not destroy its session.
Execution state: submitting -> running -> completed|failed|interrupted|unknown, potentially waiting_for_permission. Completing execution does not establish user acceptance.
`unknown` means proof is insufficient. For POST timeouts it may mean submission happened. Avoid retries without reconciliation.

## Correlation and proof gaps
OpenCode `/session/{id}/prompt_async` responds 204 without an execution ID. H1 supplies a native ascending `msg_` message ID and a local execution ID. Results require matching native session and assistant `parentID`; terminal proof requires `time.completed` plus a final finish or structured error. Idle/204 alone never proves success. Missing status without terminal proof is unknown. H1 verified these fields against the running 1.19.4 server's `/doc`. A live two-turn smoke test subsequently passed on an existing session with completed seed history, proving parent correlation, marker recall, preserved earlier history and unchanged cwd. See `H1_EVIDENCE.md`. Bridge restart loses in-memory execution records. Claude Chat integration remains untested.

## Normal Claude Chat limitations
Remote connector operates from Anthropic infrastructure. A local-only MCP port is unreachable without a secure network ingress. Tool-use iteration is possible within an active response but not guaranteed to continue after Claude's response stops. No universal documented normal-chat poll count/duration guarantee is assumed.

## Threat model and controls
- Public entry: TLS termination, token verification, OAuth-compatible issuer and claims validation for production, deny-all default.
- Credential theft: secret manager/env, rotation, no logs of tokens.
- Wrong session/cross-project: per principal allowlist of session and canonical workspace root; never trust a supplied directory as authority.
- Duplicate writes: idempotency mapping; no automatic network retry on unknown result; per-session exclusion.
- Permission bypass: never auto-grant OpenCode permissions; manual user confirmation on high-risk/destructive actions.
- Prompt injection: message history and repo files are untrusted content, never permission or policy authority.
- Audit: record user, session, tool, correlation, disposition, timestamp, not secret payload.
- Restart: in-memory executions become unknown/unrecoverable until explicit reconciliation is implemented.

## H2 security and verification boundary
The official SDK replaces the local bootstrap; deployment remains a **local development service**. H1's explicit native-session ACL, canonical workspace roots, stored-directory routing and metadata-only mutation audit logs remain enforced for every tool. Native session metadata is reauthorized for execution reads and interruption too. The adapter, execution manager, permission handling and uncertainty guards are unchanged.

Input/output structs yield strict object schemas. Raw native message parts/errors use unconstrained JSON sub-schemas to preserve upstream payloads. Successful output shapes remain compatible with H1; domain failures use a typed error alternative and `isError:true`. Schema failures are SDK-generated tool errors; malformed protocol requests are SDK protocol/HTTP errors. See CONTRACTS.

The official SDK client integration tests traverse TCP HTTP -> authentication -> MCP -> policy -> execution manager -> OpenCode HTTP fixture. A separate `cmd/mcpsmoke` exercises the same path against an explicitly selected, real existing session. This distinction and actual live-test status are recorded in H2_EVIDENCE. Reproduce quality checks and tests using the README commands.

Static bearer authentication, a single principal, in-memory execution state and exclusive native-session ownership remain limitations. Transport session expiry does not cancel accepted work. Before acceptance, official MCP cancellation propagates to upstream calls; after acceptance, only explicit authorized interruption requests native abort. No OAuth, tunnel, durable recovery or Claude Chat interoperability claim is made. Do not publish this service via a tunnel or expose it to Claude Chat yet.
