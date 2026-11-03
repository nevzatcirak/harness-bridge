# H3 — Secure Claude Chat Remote MCP Connector

Status: Codex assignment. This phase must prove actual **normal Claude Chat Custom Connector** integration, not Claude Code, Codex, or local SDK client integration.

## First: feasibility and supported interfaces
- Read `HARNESS_BRIDGE_SPEC.md`, H1/H2 evidence and current code.
- Research and cite current official Claude Custom Connector documentation, authentication/OAuth requirements, MCP transport specifications and MCP Go SDK. Do not guess compatibility or claim verified features without evidence.
- Explicitly distinguish verified locally, remote protocol verified independently, and actual Claude Chat end-to-end user interaction.
- If actual Claude Chat connector setup requires user interaction or an external feature outside terminal access, implement everything locally and stop for precise user actions. Do not fabricate a successful Claude Chat conversation.

## Scope
1. Preserve seven SDK tools and H1/H2 native semantics, single-session continuation, duplicate suppression and authorization.
2. Design authenticated HTTPS ingress without exposing OpenCode itself; expose no unauthenticated privileged listener. Prefer a minimal documented reverse proxy / tunnel using strict forwarded host/trusted proxy handling and no proprietary dependency in the Go backend. No public exposure before security review.
3. Verify Claude Chat's supported Remote MCP transport and auth model. If OAuth required, implement compliant OAuth-compatible authorization securely or document a narrowly scoped gateway solution with verified operation; never pass static bearer as OAuth without proof.
4. Enforce per-user identity, allowed project roots, session ACL, least privilege, audit without sensitive payloads, request bounds, DNS rebinding and origin checks. Explicit review needed before multi-user sharing.
5. Test remote MCP handshake, tool discovery, auth denial and session/execution behavior over actual HTTPS using a supported MCP client. No authorization bypass to make tests pass.
6. Prepare step-by-step manual connection guide for user in ordinary Claude Chat UI (not Claude Code). Include exact nonsecret configuration and commands, TLS/domain/DNS preconditions, callbacks and troubleshooting.
7. Actual E2E acceptance: in **normal Claude Chat**, discover existing OpenCode session, read history, send first prompt, inspect terminal result, send second prompt to SAME session, verify retained context/workdir. Capture redacted evidence. Until real Claude Chat run, mark this gate incomplete.
8. `gofmt`, `go build ./...`, `go test ./...`, `go vet ./...`, `go test -race ./...`. New security and integration tests required. Push Conventional Commits and update PR, do not merge.

## Gate
- [ ] Official documentation establishes exact supported Claude remote connector auth/transport.
- [ ] Privileged bridge reachable over secure authenticated HTTPS only.
- [ ] Strict authorization, logging hygiene and threat testing.
- [ ] Real SDK remote client test with TLS and invalid/expired auth scenarios.
- [ ] Operator can connect normal Claude Chat as documented.
- [ ] Actual Claude Chat ↔ Bridge ↔ existing OpenCode 2-turn proof.
- [ ] All Go quality checks, limitations and remediation documented.

Out of scope: Claude Code adapter, DB/broker, UI, Supervisor Runtime, public unauthenticated endpoints, unrestricted destructive actions.
