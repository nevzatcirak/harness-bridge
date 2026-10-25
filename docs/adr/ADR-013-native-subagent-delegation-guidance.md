# ADR-013 — Native Subagent Delegation Guidance

Date: 2026-10-25. Status: proposed for H4-A review; implemented, not deployed.

## Decision

An authorized session scopes get_harness_capabilities. Read its directory-scoped
`/agent` and installed `/doc` on demand; do not hardcode today's agent names.
Return visible primary and subagent names (mode `all` appears in both), omit
hidden implementation agents and avoid disclosing agent prompts/configuration.
Native child inspection and session creation booleans mean endpoint declarations
in that installed specification. Evidence explicitly says these are declarations,
not successful mutation tests. Bridge session creation is always false.

Native delegation is `unknown`: agent inventory establishes available subagent
names but does not establish a particular primary agent's effective Task permission
or prove a successful delegation. Do not turn inventory into a promise. The live
audit verified child-session GET, but no child existed to exercise lineage.
Parent relationships in the registry come only from authorized native metadata;
there is no child discovery that automatically expands the ACL.

MCP initialization instructions, the capability tool description and response
advise Claude Chat to ask the existing primary native agent to delegate large
exploratory/review tasks to available native subagents when appropriate and
permitted. Delegate through normal authorized send_prompt, preserving the native
permission workflow. Inspect children only if separately authorized. A native
child can exist while remaining invisible to Bridge, by design.

Claude Chat is the reasoning client; no programmable Chat subagent-spawning API
is assumed. No scheduler, independent orchestrator, delegation tool, permission
approval, native session creation or automatic follow-up loop is added. Native
status, child existence and an agent's answer never alone prove task acceptance.
