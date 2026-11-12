# Spike fixtures (ADR-021 slice 1)

NDJSON excerpts of `docs/evidence/P6_SPIKE_EVIDENCE_2026-11-10.md` (CLI 2.1.322,
subscription login, `permissionMode: default`). Lines are the raw excerpts the
evidence file prints, with these **documented additions**, because the evidence
redacted them: the `tools`, `mcp_servers` and `plugins` fields of `system/init`
(set to the configured read-only profile and empty lists), and a `session_id` on
result lines that the abbreviated excerpt did not show. The SIGINT and SIGTERM
runs are reconstructed from the evidence prose (raw capture was not retained);
their `result` line carries the values the evidence quotes. Exit codes come from
the evidence, not from these files.
