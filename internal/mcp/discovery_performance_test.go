package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nevzatcirak/harness-bridge/internal/adapters/opencode"
	"github.com/nevzatcirak/harness-bridge/internal/security"
)

func TestDiscoveryScale(t *testing.T) {
	for _, n := range []int{1, 50, 200, 1000} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			root := t.TempDir()
			var gets, statuses, permissions, inflight, peak atomic.Int64
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				current := inflight.Add(1)
				defer inflight.Add(-1)
				for old := peak.Load(); current > old && !peak.CompareAndSwap(old, current); old = peak.Load() {
				}
				switch r.URL.Path {
				case "/session/status":
					statuses.Add(1)
					w.Write([]byte(`{}`))
				case "/permission":
					permissions.Add(1)
					w.Write([]byte(`[]`))
				default:
					gets.Add(1)
					id := strings.TrimPrefix(r.URL.Path, "/session/")
					json.NewEncoder(w).Encode(opencode.Session{ID: id, Directory: root, ParentID: "ses_0000"})
				}
			}))
			defer up.Close()
			ids := []string{}
			for i := 0; i < n; i++ {
				ids = append(ids, fmt.Sprintf("ses_%04d", i))
			}
			p, _ := security.New([]string{root}, ids)
			c, _ := opencode.New(up.URL, "", "")
			s := &Server{Client: c, Policy: p}
			started := time.Now()
			out, e := s.discover(context.Background())
			elapsed := time.Since(started)
			if e != nil || len(out) != n || gets.Load() != int64(n) || statuses.Load() != 1 || permissions.Load() != 1 || peak.Load() > 4 {
				t.Fatalf("n=%d returned=%d gets=%d status=%d permissions=%d peak=%d err=%v", n, len(out), gets.Load(), statuses.Load(), permissions.Load(), peak.Load(), e)
			}
			t.Logf("sessions=%d metadata_GETs=%d status_GETs=%d permission_GETs=%d peak=%d elapsed=%s", n, gets.Load(), statuses.Load(), permissions.Load(), peak.Load(), elapsed)
			page, e := s.call(context.Background(), "find_sessions", args{Limit: 200})
			if e != nil {
				t.Fatal(e)
			}
			if len(page.(FoundSessions).Sessions) > 200 {
				t.Fatal("unbounded page")
			}
			if n == 1000 {
				p2, _ := security.New([]string{root}, append(ids, "ses_extra"))
				s2 := &Server{Client: c, Policy: p2}
				before := gets.Load()
				if _, e := s2.discover(context.Background()); e == nil || gets.Load() != before {
					t.Fatal("oversize ACL contacted native")
				}
			}
		})
	}
}
func TestDiscoveryCancellationAndIsolation(t *testing.T) {
	root := t.TempDir()
	var gets, status, permissions atomic.Int64
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/session/status":
			status.Add(1)
			w.Write([]byte(`{}`))
		case "/permission":
			permissions.Add(1)
			w.Write([]byte(`[]`))
		case "/session/ses_wait":
			gets.Add(1)
			<-r.Context().Done()
		default:
			t.Errorf("ACL isolation: %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer up.Close()
	p, _ := security.New([]string{root}, []string{"ses_wait"})
	c, _ := opencode.New(up.URL, "", "")
	s := &Server{Client: c, Policy: p}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	out, e := s.discover(ctx)
	if e == nil || len(out) != 0 || gets.Load() != 1 || status.Load() != 0 || permissions.Load() != 0 {
		t.Fatal(out, e, gets.Load())
	}
	// Deadline applies to admission queue too, without starting native requests.
	s.discoverySlots <- struct{}{}
	s.discoverySlots <- struct{}{}
	defer func() { <-s.discoverySlots; <-s.discoverySlots }()
	ctx2, cancel2 := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel2()
	if _, e := s.discover(ctx2); e == nil || gets.Load() != 1 {
		t.Fatal("queue cancellation", e)
	}
}

func TestDiscoveryDirectoryScopedEvidence(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	var statuses, permissions atomic.Int64
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/session/ses_a":
			json.NewEncoder(w).Encode(opencode.Session{ID: "ses_a", Directory: a})
		case "/session/ses_b":
			json.NewEncoder(w).Encode(opencode.Session{ID: "ses_b", Directory: b})
		case "/session/status":
			statuses.Add(1)
			switch r.URL.Query().Get("directory") {
			case a:
				w.Write([]byte(`{"ses_a":{"type":"busy"}}`))
			case b:
				w.Write([]byte(`{"ses_b":{"type":"idle"}}`))
			default:
				t.Error("missing directory")
				w.WriteHeader(400)
			}
		case "/permission":
			permissions.Add(1)
			if r.URL.Query().Get("directory") == a {
				w.Write([]byte(`[]`))
			} else if r.URL.Query().Get("directory") == b {
				w.Write([]byte(`[{"sessionID":"ses_a"}]`))
			} else {
				t.Error("missing directory")
				w.WriteHeader(400)
			}
		default:
			t.Error("unauthorized path", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer up.Close()
	p, _ := security.New([]string{a, b}, []string{"ses_a", "ses_b"})
	c, _ := opencode.New(up.URL, "", "")
	s := &Server{Client: c, Policy: p}
	out, e := s.discover(context.Background())
	if e != nil || len(out) != 2 || out[0].State != "running" || out[1].State != "idle" || statuses.Load() != 2 || permissions.Load() != 2 {
		t.Fatal(out, e, statuses.Load(), permissions.Load())
	}
}
