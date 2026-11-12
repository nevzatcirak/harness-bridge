// Package dashboard is the ADR-023 read-only session dashboard (slice 1: the
// skeleton). It is an operator-facing HTML surface on a local transport only:
// a unix socket in a 0700 directory, or a TCP listener bound to a loopback IP
// literal. It is disabled unless BRIDGE_DASHBOARD_SOCKET or
// BRIDGE_DASHBOARD_ADDR is set.
//
// The package is read-only by construction. Its route table contains only GET
// and HEAD, every other method is answered 405 before routing, and the only way
// it reaches data is the read-only Source interface. It executes no subprocess
// (in particular no git) and adds no MCP tool. Slices 2 to 5 of ADR-023
// (session views, diffs, audit ring, hardening sweep) are not implemented here.
package dashboard
