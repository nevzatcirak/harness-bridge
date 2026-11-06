package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nevzatcirak/harness-bridge/internal/adapters/opencode"
	"github.com/nevzatcirak/harness-bridge/internal/security"
)

// R82-REVIEW (#85) finding 1: when every bounded history slot is taken, the
// next check is refused with bounded.ErrBusy. That is not a completed check, so
// the session must be unknown, never idle, and carry no unfinished flag.
func TestUnfinishedBusySlotsAreUnknown(t *testing.T) {
	if n := unfinishedChecks.InFlight(); n != 0 {
		t.Fatalf("bounded history checks already in flight: %d", n)
	}
	old := unfinishedBudget
	unfinishedBudget = 200 * time.Millisecond
	t.Cleanup(func() { unfinishedBudget = old })

	root := t.TempDir()
	hanging := []string{}
	for i := 0; i < unfinishedLimit; i++ {
		hanging = append(hanging, fmt.Sprintf("ses_hang%d", i))
	}
	var entered, probeReads atomic.Int64
	stop := make(chan struct{})
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/session/status":
			w.Write([]byte(`{}`))
		case r.URL.Path == "/permission":
			w.Write([]byte(`[]`))
		case r.URL.Path == "/session/ses_a/message":
			probeReads.Add(1)
			w.Write([]byte(historyCompleted))
		case strings.HasSuffix(r.URL.Path, "/message"):
			entered.Add(1)
			select {
			case <-r.Context().Done():
			case <-stop:
			}
		default:
			id := strings.TrimPrefix(r.URL.Path, "/session/")
			json.NewEncoder(w).Encode(opencode.Session{ID: id, Directory: root})
		}
	}))
	t.Cleanup(up.Close)
	t.Cleanup(func() {
		// Free the slots before the next test: the hanging reads return and
		// their bounded calls finish.
		close(stop)
		deadline := time.Now().Add(5 * time.Second)
		for unfinishedChecks.InFlight() != 0 && time.Now().Before(deadline) {
			time.Sleep(5 * time.Millisecond)
		}
	})
	c, e := opencode.New(up.URL, "", "")
	if e != nil {
		t.Fatal(e)
	}
	p, e := security.New([]string{root}, append([]string{"ses_a"}, hanging...))
	if e != nil {
		t.Fatal(e)
	}
	s := &Server{Client: c, Policy: p}
	ctx := context.Background()

	// Fill every slot. Each caller gives up at the budget; its bounded call
	// keeps the slot until the read returns or unfinishedTimeout ends.
	var wg sync.WaitGroup
	for _, id := range hanging {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			v, e := s.call(ctx, "get_session", args{SessionID: "opencode:" + id})
			if e != nil || v.(Session).State != "unknown" {
				t.Errorf("%s: %+v %v", id, v, e)
			}
		}(id)
	}
	wg.Wait()
	deadline := time.Now().Add(2 * time.Second)
	for (entered.Load() < int64(unfinishedLimit) || unfinishedChecks.InFlight() < unfinishedLimit) && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if n := unfinishedChecks.InFlight(); n != unfinishedLimit {
		t.Fatalf("in flight = %d, want %d (slots not filled)", n, unfinishedLimit)
	}

	// ses_a has a clean history, but its check is refused: unknown, no flag,
	// and its history is never read.
	got, e := s.call(ctx, "get_session", args{SessionID: "opencode:ses_a"})
	if e != nil {
		t.Fatal(e)
	}
	b, _ := json.Marshal(got)
	m := map[string]any{}
	json.Unmarshal(b, &m)
	if m["state"] != "unknown" {
		t.Errorf("state = %v, want unknown when the check is refused (%v)", m["state"], m)
	}
	if _, present := m[unfinishedKey]; present {
		t.Errorf("%s present, want omitted (%v)", unfinishedKey, m)
	}
	if n := probeReads.Load(); n != 0 {
		t.Errorf("refused check read history %d times, want 0", n)
	}
	if n := unfinishedChecks.InFlight(); n != unfinishedLimit {
		t.Fatalf("slots freed during the probe (in flight %d); the check may not have been refused", n)
	}
}

// R82-REVIEW (#85) finding 2: the Bridge guard wins over native evidence, so a
// guarded session needs no history read. Its state is unchanged.
func TestUnfinishedGuardSkipsHistoryRead(t *testing.T) {
	for mode, want := range map[string]string{
		"running":                "running",
		"waiting_for_permission": "waiting_for_permission",
		"unknown":                "unknown",
	} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			for _, history := range []string{historyUnfinished, historyCompleted} {
				s, h := historyServer(t, root, "", heldManager(t, root, mode), `{}`, `[]`, history)
				wantUnfinished(t, s, want, false)
				if n := h.reads.Load(); n != 0 {
					t.Errorf("guarded session history reads = %d, want 0", n)
				}
			}
		})
	}
	// Without a guard the same evidence is still read and applied.
	s, h := historyServer(t, t.TempDir(), "", heldManager(t, t.TempDir(), ""), `{}`, `[]`, historyUnfinished)
	wantUnfinished(t, s, "unknown", true)
	if h.reads.Load() == 0 {
		t.Fatal("unguarded session: no history read")
	}
}
