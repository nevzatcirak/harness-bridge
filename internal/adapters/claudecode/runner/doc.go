// Package runner is the process side of the Claude Code adapter (ADR-021,
// slice 2): it spawns the child, feeds the prompt over stdin, decodes the stream
// with the slice 1 decoder, drives the section 3 state machine, appends the
// history, enforces the wall-clock cap and the child cap, and keeps the
// single-writer guard.
//
// The boundary with the pure core (package claudecode) is stated in code and
// tested: os/exec is imported by exactly one file, spawn_linux.go; the pure core
// imports nothing from this package or from the module.
//
// Slice 3 added the SIGINT, bounded wait, SIGTERM, bounded wait escalation (never a
// SIGKILL: ADR-021 section 4), Executor.Interrupt, the hardening argv and the version
// and tool-profile checks. The adapter is NOT live and is not wired into cmd/bridge or
// the MCP dispatch: exposing interrupt_execution, reading BRIDGE_CLAUDE_VERSIONS from the
// environment and the double writer lock are the wiring slice.
package runner
