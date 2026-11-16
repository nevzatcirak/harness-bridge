// Package acceptance is the driver logic of the ADR-021 slice 3 live acceptance: it runs the pinned
// Claude CLI argv through a configured wrapper, creates and removes the credential-canary decoys,
// and turns recorded NDJSON into verdict lines. It runs nothing by itself: the only entry point
// that starts a process is cmd/hb-claude-acceptance (build tag "live", never part of the default
// build). The logic here is tested against fake wrappers (shell scripts) and fake runners, with no
// network and no Claude CLI.
//
// Hard rules, each pinned by a test:
//   - the CLI is started only through the configured wrapper, never directly, never with --bare;
//   - the run budget (default 30) and --max-turns (at most 5) are enforced, and the first breach
//     stops every later run;
//   - the decoys and controls are created 0600 under the run user's HOME and removed afterwards;
//     the real .credentials.json and active_config are never named, opened or written;
//   - the evidence carries verdict lines, event types, timings and exit codes only: no transcript
//     text, no file content, no marker, nothing token-shaped (Evidence refuses such a line).
package acceptance
