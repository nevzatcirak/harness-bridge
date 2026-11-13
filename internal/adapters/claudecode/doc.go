// Package claudecode is the pure core of the Claude Code adapter (ADR-021,
// slice 1). It contains no exec: argv and environment construction, wrapper and
// directory validation, the bounded stream-json decoder, the section 3 state
// machine as a pure function, session-ID parsing, redaction and the section 5
// history record store. Nothing here starts a process, reads a Claude user's
// home, or talks to the network.
package claudecode
