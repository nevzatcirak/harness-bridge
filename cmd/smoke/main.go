// smoke requires an explicitly selected existing session; it never creates servers or sessions.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/nevzatcirak/harness-bridge/internal/adapters/opencode"
	"github.com/nevzatcirak/harness-bridge/internal/execution"
	"github.com/nevzatcirak/harness-bridge/internal/security"
)

func main() {
	if e := run(); e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}
func run() error {
	id := os.Getenv("H1_SESSION_ID")
	directory := os.Getenv("H1_DIRECTORY")
	base := os.Getenv("OPENCODE_URL")
	if !opencode.ValidID(id, "ses") || directory == "" || base == "" {
		return fmt.Errorf("OPENCODE_URL, H1_SESSION_ID (existing native ID), H1_DIRECTORY required")
	}
	p, e := security.New([]string{directory}, []string{id})
	if e != nil {
		return e
	}
	c, e := opencode.New(base, os.Getenv("OPENCODE_SERVER_USERNAME"), os.Getenv("OPENCODE_SERVER_PASSWORD"))
	if e != nil {
		return e
	}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
	session, e := c.Get(ctx, id)
	if e != nil {
		return e
	}
	if session.ID != id || session.Directory != directory || !p.Allows(id, session.Directory) {
		return fmt.Errorf("existing session identity/directory mismatch")
	}
	c = c.WithDirectory(session.Directory)
	discovered, e := c.List(ctx)
	if e != nil {
		return e
	}
	found := false
	for _, s := range discovered {
		if s.ID == id {
			found = true
		}
	}
	if !found {
		return fmt.Errorf("target absent from discovery")
	}
	before, e := c.History(ctx, id)
	if e != nil {
		return e
	}
	fmt.Printf("existing session selected; native ID and directory redacted; history before prompts: %d messages\n", len(before))
	var nonce [8]byte
	if _, e = rand.Read(nonce[:]); e != nil {
		return e
	}
	marker := "H1-" + hex.EncodeToString(nonce[:])
	m := execution.New(c)
	defer m.Close()
	var executions []execution.Execution
	prompts := []string{"Continuity smoke test. Do not use tools or change files. Remember the exact marker " + marker + " and reply with only that marker.", "Continuity smoke test. Do not use tools or change files. Reply with only the exact marker from my immediately preceding prompt."}
	for turn, prompt := range prompts {
		x, e := m.Send(ctx, id, prompt, marker+fmt.Sprint(turn))
		if e != nil {
			return e
		}
		duplicate, e := m.Send(ctx, id, prompt, marker+fmt.Sprint(turn))
		if e != nil || duplicate.ID != x.ID {
			return fmt.Errorf("duplicate suppression failed")
		}
		if _, e = m.Send(ctx, id, prompt+" changed", marker+fmt.Sprint(turn)); e == nil {
			return fmt.Errorf("idempotency conflict accepted")
		}
		for {
			x, e = m.Get(ctx, x.ID)
			if e != nil {
				return e
			}
			if x.State == "completed" {
				break
			}
			if x.State == "failed" || x.State == "interrupted" || x.State == "waiting_for_permission" {
				return fmt.Errorf("turn %d: %s", turn+1, x.State)
			}
			if time.Since(x.Started) > 5*time.Minute {
				return fmt.Errorf("turn %d unknown/timed out; do not resubmit blindly", turn+1)
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(250 * time.Millisecond):
			}
		}
		_, msgs, e := m.Result(ctx, x.ID)
		if e != nil {
			return e
		}
		output := ""
		for _, msg := range msgs {
			if msg.Info.Role == "assistant" {
				if msg.Info.ParentID != x.MessageID {
					return fmt.Errorf("wrong parent ID")
				}
				if msg.Info.Path.Cwd != directory {
					return fmt.Errorf("assistant cwd changed")
				}
				for _, raw := range msg.Parts {
					var part struct{ Type, Text string }
					_ = json.Unmarshal(raw, &part)
					if part.Type == "text" {
						output += part.Text
					}
				}
			}
		}
		if strings.TrimSpace(output) != marker {
			return fmt.Errorf("turn %d context marker mismatch", turn+1)
		}
		executions = append(executions, x)
		fmt.Printf("turn %d completed; assistant parentID, completion marker, context marker and cwd verified (IDs/content redacted)\n", turn+1)
	}
	after, e := c.History(ctx, id)
	if e != nil {
		return e
	}
	ids := map[string]bool{}
	for _, msg := range after {
		ids[msg.Info.ID] = true
	}
	for _, msg := range before {
		if !ids[msg.Info.ID] {
			return fmt.Errorf("earlier history missing")
		}
	}
	for _, x := range executions {
		if !ids[x.MessageID] {
			return fmt.Errorf("prompt missing from final history")
		}
	}
	final, e := c.Get(ctx, id)
	if e != nil {
		return e
	}
	if final.ID != id || final.Directory != directory {
		return fmt.Errorf("session identity or directory changed")
	}
	fmt.Printf("PASS: same existing native session; earlier history retained; both turns present; directory unchanged; final history: %d messages\n", len(after))
	return nil
}
