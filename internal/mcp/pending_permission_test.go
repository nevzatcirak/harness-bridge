package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nevzatcirak/harness-bridge/internal/adapters/opencode"
	"github.com/nevzatcirak/harness-bridge/internal/security"
)

// get_session exposes only pending_permission_count, only while waiting, only
// for this session, and never a request ID, tool, pattern or native text.
func TestGetSessionPendingPermissionCountOnly(t *testing.T) {
	const secretText = "SECRET-NATIVE-PATTERN-/etc/shadow"
	for _, tc := range []struct {
		name, permissions string
		wantState         string
		wantCount         int // 0 = field absent
	}{
		{"two for this session, one foreign", `[{"id":"per_1","sessionID":"ses_a","permission":"edit","patterns":["` + secretText + `"]},{"id":"per_2","sessionID":"ses_a","permission":"bash","patterns":[]},{"id":"per_3","sessionID":"ses_b","permission":"edit","patterns":[]}]`, "waiting_for_permission", 2},
		{"only foreign", `[{"id":"per_3","sessionID":"ses_b","permission":"edit","patterns":[]}]`, "idle", 0},
		{"none", `[]`, "idle", 0},
		{"read error", `error`, "unknown", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/session/status":
					w.Write([]byte(`{}`))
				case "/permission":
					if tc.permissions == "error" {
						w.WriteHeader(500)
						return
					}
					w.Write([]byte(tc.permissions))
				case "/session/ses_a/message":
					w.Write([]byte(`[]`))
				default:
					json.NewEncoder(w).Encode(opencode.Session{ID: "ses_a", Directory: root})
				}
			}))
			defer up.Close()
			c, _ := opencode.New(up.URL, "", "")
			p, _ := security.New([]string{root}, []string{"ses_a", "ses_b"})
			s := &Server{Client: c, Policy: p}
			v, e := s.call(context.Background(), "get_session", args{SessionID: "opencode:ses_a"})
			if e != nil {
				t.Fatal(e)
			}
			sess := v.(Session)
			if sess.State != tc.wantState {
				t.Fatalf("state = %s", sess.State)
			}
			if tc.wantCount == 0 && sess.PendingPermissionCount != nil {
				t.Fatalf("count present: %d", *sess.PendingPermissionCount)
			}
			if tc.wantCount > 0 && (sess.PendingPermissionCount == nil || *sess.PendingPermissionCount != tc.wantCount) {
				t.Fatalf("count = %v want %d", sess.PendingPermissionCount, tc.wantCount)
			}
			raw, _ := json.Marshal(v)
			for _, banned := range []string{secretText, "per_1", "per_2", "patterns", "pending_permission\""} {
				if strings.Contains(string(raw), banned) {
					t.Fatalf("serialized session leaks %q: %s", banned, raw)
				}
			}
		})
	}
}
