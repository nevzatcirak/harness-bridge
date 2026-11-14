package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"time"

	"github.com/nevzatcirak/harness-bridge/internal/admin"
)

// openAdmin opens the ADR-020 (b) admin socket in writer mode only, after
// writer ownership is acquired. It returns nil when BRIDGE_ADMIN_SOCKET is
// unset or the ingress is read-only.
func openAdmin(env func(string) string, readOnly bool, runs admin.Releaser, claude admin.ClaudeReleaser) (*admin.Server, error) {
	path := env("BRIDGE_ADMIN_SOCKET")
	if path == "" {
		return nil, nil
	}
	if readOnly {
		log.Printf("BRIDGE_ADMIN_SOCKET ignored: read-only ingress opens no admin socket")
		return nil, nil
	}
	uid := os.Geteuid()
	ln, err := admin.Listen(path, uid)
	if err != nil {
		return nil, err
	}
	srv := &admin.Server{Runs: runs, UID: uid}
	if claude != nil {
		srv.Claude = claude // nil otherwise: a claudecode: release is refused "not enabled"
	}
	go func() {
		if err := srv.Serve(ln); err != nil {
			log.Printf("admin socket stopped: %v", err)
		}
	}()
	return srv, nil
}

const adminUsage = `usage: bridge admin release-guard --session opencode:ses_<id>|claudecode:<uuid> --execution <execution_id> --ticket <ticket> [--allow-missing-user-message] [--socket <path>]

Releases the busy guard of one session through the writer's private admin
socket (ADR-020 b; for claudecode: sessions the Claude extension of ADR-021
slice 4, which has no native read and is refused while no Claude Code adapter is
running; --allow-missing-user-message does not apply to it). The socket defaults to $BRIDGE_ADMIN_SOCKET. Run as the
service user (sudo -u <service user>). --ticket must match [A-Za-z0-9._-]{1,64}.
Exit status: 0 released, 1 refused or unreachable, 2 usage error.
`

// runAdmin is the "admin" client subcommand. It prints the JSON evidence.
func runAdmin(args []string, env func(string) string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "release-guard" {
		fmt.Fprint(stderr, adminUsage)
		return 2
	}
	fs := flag.NewFlagSet("release-guard", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, adminUsage) }
	session := fs.String("session", "", "global session ID (opencode:ses_… or claudecode:<uuid>)")
	executionID := fs.String("execution", "", "execution ID holding the guard")
	ticket := fs.String("ticket", "", "ticket ID, [A-Za-z0-9._-]{1,64}")
	allowMissing := fs.Bool("allow-missing-user-message", false, "release although the correlated user message is absent (audited)")
	socket := fs.String("socket", env("BRIDGE_ADMIN_SOCKET"), "admin socket path")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	if fs.NArg() != 0 || *session == "" || *executionID == "" || *socket == "" {
		fmt.Fprint(stderr, adminUsage)
		return 2
	}
	if !admin.ValidTicket(*ticket) {
		fmt.Fprintln(stderr, "--ticket must match [A-Za-z0-9._-]{1,64}")
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	resp, err := admin.Call(ctx, *socket, admin.Request{Op: admin.OpReleaseGuard, SessionID: *session, ExecutionID: *executionID, Ticket: *ticket, AllowMissingUserMessage: *allowMissing})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	out, _ := json.MarshalIndent(resp, "", "  ")
	fmt.Fprintln(stdout, string(out))
	if resp.Outcome != "released" {
		return 1
	}
	return 0
}
