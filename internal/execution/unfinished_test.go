package execution

import (
	"context"
	"errors"
	"testing"

	"github.com/nevzatcirak/harness-bridge/internal/adapters/opencode"
)

// F-5: admission and discovery share opencode.UnfinishedAssistant. Admission
// still refuses a session whose native history has an assistant message
// without time.completed, even when native status is absent (inferred idle).
func TestAdmissionRefusesUnfinishedAssistant(t *testing.T) {
	ctx := context.Background()
	for _, native := range []string{"absent", "idle"} {
		t.Run(native, func(t *testing.T) {
			orphan := opencode.Message{}
			orphan.Info.ID = "msg_orphan"
			orphan.Info.Role = "assistant"
			orphan.Info.SessionID = "ses_test"
			f := &fake{status: "idle", missing: native == "absent", messages: []opencode.Message{orphan}}
			m := setup(t, f)
			_, err := m.Send(ctx, "ses_test", "prompt", "unfinished-key-"+native)
			var typed *Error
			if !errors.As(err, &typed) || typed.Code != "SESSION_BUSY" || typed.Message != "unfinished native assistant message" {
				t.Fatalf("err = %v, want SESSION_BUSY unfinished native assistant message", err)
			}
			if f.sends != 0 {
				t.Fatalf("sends = %d, want 0", f.sends)
			}
			wantGuard(t, m, false, "")
			// Once the message completes, the same session is admitted.
			f.mu.Lock()
			f.messages[0].Info.Time.Completed = 1
			f.mu.Unlock()
			if _, err := m.Send(ctx, "ses_test", "prompt", "completed-key-"+native); err != nil {
				t.Fatal(err)
			}
		})
	}
}
