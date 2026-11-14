package admin

import (
	"os/exec"
	"strings"
	"testing"
)

// #257/#259 P3-5: the admin channel names the Claude release types from the pure core and does
// not link the process side (runner, os/exec) of the adapter.
func TestAdminDoesNotLinkTheRunner(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", "-f", "{{.ImportPath}}", ".").Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	deps := strings.Fields(string(out))
	for _, bad := range []string{"os/exec", "github.com/nevzatcirak/harness-bridge/internal/adapters/claudecode/runner"} {
		for _, d := range deps {
			if d == bad {
				t.Errorf("internal/admin links %s", bad)
			}
		}
	}
}
