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
- ADR-007: Official `github.com/modelcontextprotocol/go-sdk` v1.8.0 chosen for production MCP. Current stdlib MCP subset is a local bootstrap because this environment cannot fetch Go modules; it is not certified for Claude Custom Connector.

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
OpenCode `/session/{id}/prompt_async` responds 204 without an execution ID. H1 supplies a native ascending `msg_` message ID and a local execution ID. Results require matching native session and assistant `parentID`; terminal proof requires `time.completed` plus a final finish or structured error. Idle/204 alone never proves success. Missing status without terminal proof is unknown. H1 verified these fields against the running 1.19.4 server's `/doc`; live model continuation remains blocked because discovery found no existing sessions. See `H1_EVIDENCE.md`. Bridge restart loses in-memory execution records. A two-turn mock test is not real model-context proof.

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

## Security status of prototype
The stdlib bootstrap is a **local development prototype**. H1 adds an explicit native-session ACL for the single token principal, canonical workspace root policy, directory routing, and metadata-only mutation audit logs. It retains a static bearer token and loopback binding. OAuth, multiple principals and full MCP protocol compatibility remain out of scope. Do not publish it via a tunnel or expose it to Claude Chat yet.
