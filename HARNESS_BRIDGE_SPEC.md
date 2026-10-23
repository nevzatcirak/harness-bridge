# Harness Bridge — Detailed Project Specification

**Status:** Initial technical baseline; version-dependent integration claims must be validated against current official documentation.

## 1. Responsibility and engineering approach

The project assistant acts as Principal Software Architect, AI Agent Infrastructure Engineer, and Technical Lead. It researches current MCP, Claude Chat, Claude Code, and OpenCode functionality; makes and records explicit decisions; produces testable production-quality Go code; identifies protocol, authentication, permission, state-management, security, and concurrency risks; and challenges unnecessary complexity. Communication is in Turkish, with standard English technical terminology. Working software takes priority over architectural speculation.

Clearly distinguish confirmed facts from proposed design, experimental behavior, and unverified assumptions. Do not fabricate endpoints or API semantics; research versions and official sources before integration work.

## 2. Vision and user experience

Harness Bridge is an **independent, local-first software tool** enabling **Claude Chat (normal conversational UI)** to supervise and continue existing sessions of coding-agent harnesses such as OpenCode and Claude Code.

The user should be able to ask Claude Chat to:

1. Discover existing harness sessions.
2. Identify/select the correct session.
3. Read earlier messages and execution context.
4. Send new instructions into the selected **existing** session.
5. Monitor execution lifecycle without indefinite blocking.
6. Detect completion, failure, interruption, disconnection and permission waits.
7. Inspect resulting output and verification evidence.
8. Evaluate whether user requirements and acceptance criteria are satisfied.
9. Send corrective follow-ups to the same session as needed.
10. Return a grounded final report or explain what intervention is required.

**The defining requirement is persistent session continuation, not one-shot code-agent invocation.** Preserve the native session's conversation history and working directory.

## 3. Boundaries and non-goals

- Keep Harness Bridge independent of PIRI, ARIF, and any other orchestration platform.
- Do not build a general-purpose multi-agent orchestration platform in MVP.
- Do not introduce a database, message broker, browser frontend, Kubernetes, or a separate LLM-based supervisor without a demonstrated requirement.
- Claude Chat through a Custom Remote MCP Connector is the main user interface.
- Normal Claude Chat, Claude Code's native Code interface, and Claude Code Remote Control are different capabilities and must not be conflated.
- An optional external autonomous Supervisor Runtime may be introduced only if background/durable/event-driven orchestration is required.

## 4. Target architecture

```text
Claude Chat (normal chat)
    -> Custom Remote MCP Connector (authenticated HTTPS)
    -> Harness Bridge MCP Server
    -> Session Registry / Session Manager / Execution Monitor
    -> Harness Adapter (OpenCode first, Claude Code later)
    -> Existing harness server and session
```

Components and responsibilities:

| Component | Responsibility |
|---|---|
| MCP Server | Stable capability interface exposed to Claude Chat |
| Session Registry | Discover native sessions and map stable global identities |
| Harness Adapters | Encapsulate harness-specific transport and session semantics |
| Session Manager | Session access, lifecycle and concurrency guards |
| Execution Monitor | Asynchronous starts, status, interruption, timeout and result tracking |
| Result Normalizer | Convert harness-specific messages/results to common shape |
| Security Layer | Authentication, authorization, path and operation policy, audit |

Prefer one modular Go binary; keep integration boundaries meaningful without premature abstraction.

## 5. Phase 1: OpenCode adapter

Use OpenCode's **currently documented** HTTP API and official SDK when appropriate. Verify available endpoints, parameters, error responses and event semantics before coding.

Required capabilities:
- Existing session discovery and metadata.
- Reading message history.
- Sending a new prompt to an existing session.
- Asynchronous execution and correlation IDs.
- Execution status and result retrieval.
- Error and interruption reporting.
- Explicitly authorized interruption.

Distinguish four independent concepts: OpenCode session, running OpenCode server process, attached TUI client, and in-flight execution. Launching a new server does **not** imply control over a pre-existing interactive terminal process. Document any API capability gap rather than hiding it behind an abstraction.

## 6. Phase 2: Claude Code adapter

Investigate the latest **supported** Claude Code CLI and Agent SDK for: discovering resumable sessions, permitted history access, resuming a specific session, sending follow-up instructions, and collecting/monitoring results. Distinguish **resuming a historical conversation** from **controlling an actively running interactive terminal**. Do not claim the latter unless a documented or directly verified supported mechanism exists.

## 7. MCP tool interface

Proposed initial tool surface:

| Tool | Role |
|---|---|
| `list_sessions` | Discover visible sessions, with harness and metadata filters |
| `get_session` | Inspect one session's identity, context and status |
| `read_session_history` | Read bounded/paginated prior messages |
| `send_prompt` | Submit prompt into selected session and return request/execution correlation |
| `get_execution_status` | Observe an execution's lifecycle state |
| `get_execution_result` | Obtain output, artifacts, error information and evidence |
| `interrupt_execution` | Request cancellation if permission/policy allows |

Use `harness_type + native_session_id` as stable global session identity (precise encoding to be decided). For long calls return an **execution ID**, not an indefinitely blocked tool request. Define error envelopes, pagination limits, idempotency keys, authorization behavior and versioning during contract design. Tools beyond this set require concrete use cases.

## 8. Lifecycle and concurrency

Represent **session state** and **execution state** separately, and support states including `idle`, `running`, `waiting_for_permission`, `completed`, `failed`, `interrupted`, `unknown` in their appropriate domains.

Track at least request/execution correlation, start/end/update timestamps, normalized results and failure categories. Idle/stopped session state is **not** proof of task success. Guard each session against conflicting simultaneous runs; when appropriate reject as busy or serialize deliberately. Prevent duplicate prompt submission on retries; define semantics for network loss, execution timeout, context cancellation, bridge restart, uncertain completion and cleanup. Preserve the original session's working directory and relevant conversation context.

## 9. Default supervisor behavior

Claude Chat is the default reasoning supervisor:

**Discover -> Read -> Plan -> Send -> Monitor -> Evaluate -> Continue / Finish**

Requirements:
- Reuse the correct pre-existing session.
- Define measurable acceptance criteria before implementation work.
- Monitor for meaningful terminal execution state; do not conflate harness stoppage with requirement satisfaction.
- Inspect file changes, logs, tests and verification evidence where available.
- Send follow-up prompts only when indicated by evidence.
- Enforce request budgets, user confirmation and harness-level permissions.
- MVP self-correction limit: at most five iterations unless the user specifies another limit.
- Claude Chat cannot be assumed to reawaken automatically after a response completes. Durable background/event-driven orchestration needs a different runtime and explicit design.

## 10. Security model

Harness Bridge can direct agents that modify files and execute commands, so treat its endpoints as privileged.

- Bind local harness API endpoints to loopback by default.
- Never expose an unauthenticated MCP server to the public internet.
- Use authenticated HTTPS for remote access; prefer standards-aligned OAuth for external MCP connections when supported/required.
- Authorize users, harnesses and individual session identities; prevent cross-session access and selection.
- Require allowlisted project roots, least-privilege process configuration and defense in depth.
- Protect credentials and tokens; never log secrets or sensitive file contents.
- Require explicit authorization for destructive/high-risk operations.
- Audit state-changing operations and their authorization outcomes.
- Resist prompt injection through repository files, tool output, logs, or messages originating from the harness.
- Do not bypass harness-level permission controls; permission waiting is an explicit lifecycle condition.
- Handle conflicting concurrent executions safely and deterministically.

Threat model must cover: externally reachable connector, credential theft, wrong-session selection, cross-project path access, prompt injection, duplicated writes, stale execution states and cancellation race conditions.

## 11. Engineering standards and repository layout

**Backend language: Go, latest stable version.** Do not add Python/Node or other backend languages without explicit user approval and a compelling technical requirement.

Preferred stack: `github.com/modelcontextprotocol/go-sdk` if actively maintained and suitable; Go `net/http` server/client; Go structs/JSON; `context.Context`; goroutines, channels and mutexes; `log/slog`; environment-variable configuration; standard `testing` package. Use minimal third-party dependencies. Docker support is optional; distribute a single compiled executable.

Suggested layout:

```text
cmd/bridge/
internal/mcp/
internal/adapters/
internal/session/
internal/execution/
internal/security/
internal/config/
```

Require idiomatic Go code, meaningful interfaces, secure defaults, cancellation/deadlines, error classification, concurrency safety and graceful shutdown. Run `gofmt`, `go vet ./...`, `go test ./...`, and `go test -race ./...` as applicable. Include unit and integration tests with clear external dependency requirements.

## 12. Documents and decisions

Maintain:
1. PRD.
2. Architecture Decision Records (ADRs).
3. System architecture and integration constraints.
4. API and MCP tool contracts.
5. Session and execution lifecycle state models.
6. Threat model.
7. Phased implementation plan.
8. Executable source code.
9. Unit/integration tests.
10. Install/configuration guidance.
11. Claude Custom Connector setup instructions.
12. End-to-end demonstration script.

Do not repeatedly reopen recorded architectural decisions absent new evidence. Review code for correctness, safety, security, reliability, maintainability, and observed versus assumed behavior. Provide complete, executable examples when implementation is requested. Only request a user decision when progress would otherwise be blocked or architecture changes materially.

## 13. MVP acceptance criteria

Demonstrate from a **real Claude Chat conversation**:
1. Connect to a pre-existing OpenCode server.
2. Discover/select a pre-existing session.
3. Read its history.
4. Send prompt #1 to that session.
5. Monitor and identify meaningful execution completion.
6. Fetch output and evidence.
7. Evaluate completion against the user's requirements.
8. Send prompt #2 to the **same session** if required.
9. Verify continuity of session history, working directory and context.
10. Produce an accurate final report with any caveats.

Include negative tests for duplicate submission, busy sessions, connection loss, timeouts, unauthorized access and interrupted or uncertain executions.

## 14. Development phases

- **0 — Feasibility:** Verify latest OpenCode session API, SDK, transport and Claude Custom Connector requirements; document unknowns.
- **1 — OpenCode local proof:** Demonstrate connection to an existing server and two-prompt same-session continuation.
- **2 — MCP surface:** Implement proposed tools with robust contracts and lifecycle control.
- **3 — Claude integration:** Set up authenticated remote connectivity to Claude Chat; test the real product behavior.
- **4 — Supervisor loop:** Monitor, evaluate, retry within bounds, verify evidence and test recovery paths.
- **5 — Claude Code:** Add the documented supported session continuation mechanisms.
- **6 — Optional improvements:** Consider durable supervisor, persistence, or operational extras only when justified.

For every phase, document goal, dependencies, risks, acceptance criteria, code/tests, confirmed functionality and unsupported limitations.

## 15. First priority and milestone

**First verify current official OpenCode session APIs and Claude Chat Custom Connector behavior.** Then deliver the smallest working OpenCode-to-Claude Chat experiment. The first milestone is not a diagram or an API mock: it is a real Claude Chat conversation sending a prompt to an existing OpenCode session, observing its completed result, and sending a second prompt to the same session while preserving prior context.