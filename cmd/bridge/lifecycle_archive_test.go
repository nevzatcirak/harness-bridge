package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nevzatcirak/harness-bridge/internal/adapters/opencode"
	"github.com/nevzatcirak/harness-bridge/internal/execution"
	"github.com/nevzatcirak/harness-bridge/internal/mcpclient"
	"github.com/nevzatcirak/harness-bridge/internal/security"
)

// ADR-025 4.5 / D18 Q1 through the production wiring (task #383, review #379): an archived session
// refuses send_prompt in every process that can send, and the catalog hides it. The tests build the real
// lifecycle wiring (openLifecycle + apply) on a fake native server and a real overlay directory; two
// wirings on one directory are two processes as far as the overlay is concerned (one lock holder, one
// reader).

const (
	sesA = "ses_00000000000000000000000001" // the archived session
	sesB = "ses_00000000000000000000000002" // never archived
)

type lcStack struct {
	t    *testing.T
	root string
	runs *execution.Manager
	cs   *sdk.ClientSession
}

// newLCStack applies w to a server built the way main builds it. The native server answers the reads the
// catalog makes (idle sessions in root); the manager's runner accepts prompts.
func newLCStack(t *testing.T, w *lifecycleWiring, readOnly bool) *lcStack {
	t.Helper()
	root := realDir(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/session/status":
			_, _ = rw.Write([]byte(`{}`))
		case r.URL.Path == "/permission", strings.HasSuffix(r.URL.Path, "/message"):
			_, _ = rw.Write([]byte(`[]`))
		default:
			id := strings.TrimPrefix(r.URL.Path, "/session/")
			_ = json.NewEncoder(rw).Encode(opencode.Session{ID: id, Title: id, Directory: root})
		}
	}))
	t.Cleanup(upstream.Close)
	client, err := opencode.New(upstream.URL, "", "")
	if err != nil {
		t.Fatal(err)
	}
	runs := execution.NewWithTiming(&relayRunner{directory: root}, 30*time.Second, 50*time.Millisecond)
	t.Cleanup(runs.Close)
	pol, err := security.New([]string{root}, []string{sesA, sesB})
	if err != nil {
		t.Fatal(err)
	}
	srv := newMCPServer(client, runs, strings.Repeat("t", 32), pol, nil, readOnly, nil)
	w.apply(srv, runs)
	front := httptest.NewServer(srv)
	t.Cleanup(front.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cs, err := mcpclient.Connect(ctx, front.URL, srv.Token)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return &lcStack{t: t, root: root, runs: runs, cs: cs}
}

var sendKey int

// send submits a prompt to the manager directly (the tool's own path) and returns the error.
func (s *lcStack) send(session string) error {
	s.t.Helper()
	sendKey++
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := s.runs.Send(ctx, session, "hello", fmt.Sprintf("hb-archive-test-%06d", sendKey))
	return err
}

func archivedRefusal(err error) bool {
	var e *execution.Error
	return errors.As(err, &e) && e.Code == "FORBIDDEN" && e.Reason == "session_archived"
}

// catalogIDs lists the catalog through MCP and returns the native IDs and whether each is flagged archived.
func (s *lcStack) catalogIDs(includeArchived bool) map[string]bool {
	s.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	r, err := s.cs.CallTool(ctx, &sdk.CallToolParams{Name: "list_catalog", Arguments: map[string]any{"include_archived": includeArchived}})
	if err != nil || r.IsError {
		s.t.Fatalf("list_catalog: %v %+v", err, r)
	}
	raw, _ := json.Marshal(r.StructuredContent)
	var out struct {
		Sessions []struct {
			SessionRef struct {
				SessionID string `json:"session_id"`
			} `json:"session_ref"`
			Archived bool `json:"archived"`
		} `json:"sessions"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		s.t.Fatal(err)
	}
	got := map[string]bool{}
	for _, e := range out.Sessions {
		got[strings.TrimPrefix(e.SessionRef.SessionID, "opencode:")] = e.Archived
	}
	return got
}

func archiveViaStore(t *testing.T, w *lifecycleWiring, id string) {
	t.Helper()
	if w.store == nil || !w.store.CanWrite() {
		t.Fatal("this wiring does not hold the overlay write role")
	}
	if _, archived, err := w.store.Archive(context.Background(), "opencode:"+id, "op"); err != nil || !archived {
		t.Fatalf("archive %s: archived=%v err=%v", id, archived, err)
	}
}

// The lock holder refuses send_prompt to an archived session (Install, ADR-025 4.5, D18 Q1), and still
// accepts a session that is not archived. Removing w.archive.Install(runs) turns this RED.
func TestLifecycleLockHolderRefusesSendToAnArchivedSession(t *testing.T) {
	env, _ := lcEnv(t, newOverlayDir(t))
	w, err := openLifecycle(env, os.Geteuid(), false)
	if err != nil || w == nil || w.archive == nil {
		t.Fatalf("lock holder: %v %v", w, err)
	}
	t.Cleanup(w.Close)
	st := newLCStack(t, w, false)
	if err := st.send(sesB); err != nil {
		t.Fatalf("a session that is not archived was refused: %v", err)
	}
	archiveViaStore(t, w, sesA)
	if err := st.send(sesA); !archivedRefusal(err) {
		t.Fatalf("send to an archived session: %v, want FORBIDDEN session_archived", err)
	}
}

// A writer that did not get the overlay lock is a reader (ADR-025 4.3): it registers no archive tool,
// but an archived session still refuses send_prompt there (D18 Q1), within the poll bound of the
// refresh (one stat poll a second, driven by a 250 ms tick: about 1.25 s). RED before the fix: no check
// was installed, so the prompt was accepted for as long as the process ran.
func TestLifecycleReaderRefusesSendToAnArchivedSessionWithinTheBound(t *testing.T) {
	dir := newOverlayDir(t)
	env, _ := lcEnv(t, dir)
	holder, err := openLifecycle(env, os.Geteuid(), false)
	if err != nil || holder == nil || holder.archive == nil {
		t.Fatalf("holder: %v %v", holder, err)
	}
	t.Cleanup(holder.Close)
	second, err := openLifecycle(env, os.Geteuid(), false)
	if err != nil || second == nil || second.store == nil || second.store.CanWrite() {
		t.Fatalf("second writer: %v %v", second, err)
	}
	t.Cleanup(second.Close)
	st := newLCStack(t, second, false)
	if err := st.send(sesB); err != nil {
		t.Fatalf("a session that is not archived was refused: %v", err)
	}
	archiveViaStore(t, holder, sesA)
	start := time.Now()
	deadline := start.Add(3 * time.Second)
	for {
		err := st.send(sesA)
		if archivedRefusal(err) {
			t.Logf("refused %v after the archive", time.Since(start).Round(10*time.Millisecond))
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("send to an archived session was still not refused 3 s after the archive (last: %v)", err)
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err := st.send(sesB); archivedRefusal(err) {
		t.Fatalf("a session that is not archived was refused as archived: %v", err)
	}
	// Un-archiving is seen as well: the refusal is the overlay's state, not a latch.
	if _, err := holder.store.Unarchive(context.Background(), "opencode:"+sesA); err != nil {
		t.Fatal(err)
	}
	for end := time.Now().Add(3 * time.Second); ; time.Sleep(100 * time.Millisecond) {
		if err := st.send(sesA); !archivedRefusal(err) {
			break
		}
		if time.Now().After(end) {
			t.Fatal("an un-archived session still refuses send_prompt 3 s after the un-archive")
		}
	}
}

// An unknown overlay state keeps the last good snapshot (ADR-025 4.4): a reader that has seen the
// session archived keeps refusing it when the file becomes unreadable, and does not refuse every other
// prompt ("an unknown state never blocks unrelated prompts").
func TestLifecycleReaderKeepsTheLastGoodSnapshotWhenTheFileGoesBad(t *testing.T) {
	dir := newOverlayDir(t)
	env, _ := lcEnv(t, dir)
	holder, err := openLifecycle(env, os.Geteuid(), false)
	if err != nil || holder == nil {
		t.Fatalf("holder: %v %v", holder, err)
	}
	t.Cleanup(holder.Close)
	second, err := openLifecycle(env, os.Geteuid(), false)
	if err != nil || second == nil || second.store.CanWrite() {
		t.Fatalf("second writer: %v %v", second, err)
	}
	t.Cleanup(second.Close)
	st := newLCStack(t, second, false)
	archiveViaStore(t, holder, sesA)
	for end := time.Now().Add(3 * time.Second); !archivedRefusal(st.send(sesA)); time.Sleep(100 * time.Millisecond) {
		if time.Now().After(end) {
			t.Fatal("the reader never saw the archive")
		}
	}
	// Replace the file with garbage (a new inode, so the stat poll sees it).
	bad := filepath.Join(dir, "overlay.json.bad")
	if err := os.WriteFile(bad, []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(bad, filepath.Join(dir, "overlay.json")); err != nil {
		t.Fatal(err)
	}
	for end := time.Now().Add(4 * time.Second); second.store.Err() == nil; time.Sleep(100 * time.Millisecond) {
		if time.Now().After(end) {
			t.Fatal("the reader never noticed the bad file")
		}
	}
	if err := st.send(sesA); !archivedRefusal(err) {
		t.Fatalf("the archived session is no longer refused after the file went bad: %v", err)
	}
	if err := st.send(sesB); archivedRefusal(err) {
		t.Fatalf("an unknown overlay state blocked an unrelated prompt: %v", err)
	}
}

// Close stops the poller (no goroutine outlives the wiring) and is idempotent.
func TestLifecycleCloseStopsThePoller(t *testing.T) {
	dir := newOverlayDir(t)
	env, _ := lcEnv(t, dir)
	holder, err := openLifecycle(env, os.Geteuid(), false)
	if err != nil || holder == nil {
		t.Fatal(err)
	}
	t.Cleanup(holder.Close)
	second, err := openLifecycle(env, os.Geteuid(), false)
	if err != nil || second == nil {
		t.Fatal(err)
	}
	newLCStack(t, second, false)
	if second.done == nil {
		t.Fatal("a reader that can send has no poller: its archive snapshot would never refresh")
	}
	second.Close()
	select {
	case <-second.done:
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not stop the poller")
	}
	second.Close() // idempotent
	// The lock holder and a wiring without an overlay need no poller.
	if holder.done != nil {
		t.Error("the lock holder started a poller")
	}
}

// The catalog hides an archived session through the production wiring (w.catalog.Overlay = store) and
// lists it, flagged, with include_archived. Removing that assignment turns this RED.
func TestLifecycleCatalogHidesAnArchivedSessionThroughTheWiring(t *testing.T) {
	env, _ := lcEnv(t, newOverlayDir(t))
	w, err := openLifecycle(env, os.Geteuid(), false)
	if err != nil || w == nil {
		t.Fatalf("wiring: %v %v", w, err)
	}
	t.Cleanup(w.Close)
	st := newLCStack(t, w, false)
	if got := st.catalogIDs(false); len(got) != 2 {
		t.Fatalf("catalog before the archive: %v, want both sessions", got)
	}
	archiveViaStore(t, w, sesA)
	got := st.catalogIDs(false)
	if _, ok := got[sesA]; ok || len(got) != 1 {
		t.Fatalf("catalog after the archive: %v, want only %s", got, sesB)
	}
	all := st.catalogIDs(true)
	if archived, ok := all[sesA]; !ok || !archived {
		t.Fatalf("include_archived: %v, want %s listed and flagged archived", all, sesA)
	}
}

// Read-only ingress (decision): send_prompt is refused at call time (the ingress never sends), so no
// archive check is installed there; the overlay is opened as a reader and the catalog still hides the
// archived sessions. An invalid overlay file is not a startup error for a reader (D18 Q3): the process
// starts, the state is unknown, list_catalog fails instead of listing a guess.
func TestLifecycleReadOnlyStartsWithAnInvalidOverlayFileAndReportsUnknown(t *testing.T) {
	dir := newOverlayDir(t)
	if err := os.WriteFile(filepath.Join(dir, "overlay.json"), []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	env, _ := lcEnv(t, dir)
	if w, err := openLifecycle(env, os.Geteuid(), false); err == nil {
		w.Close()
		t.Fatal("a writer started with an invalid overlay file")
	}
	w, err := openLifecycle(env, os.Geteuid(), true)
	if err != nil || w == nil || w.store == nil {
		t.Fatalf("read-only with an invalid overlay file: %v %v, want a start", w, err)
	}
	t.Cleanup(w.Close)
	if w.store.CanWrite() {
		t.Fatal("read-only holds the write role")
	}
	if w.store.Err() == nil {
		t.Fatal("the state of an invalid overlay file is not reported unknown")
	}
	if _, aerr := w.store.Archived(context.Background(), "opencode:"+sesA); aerr == nil {
		t.Fatal("Archived answered for an unknown overlay state")
	}
	st := newLCStack(t, w, true)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	r, err := st.cs.CallTool(ctx, &sdk.CallToolParams{Name: "list_catalog", Arguments: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	if !r.IsError {
		t.Fatalf("list_catalog listed with an unknown overlay state: %+v", r.StructuredContent)
	}
	if w.done != nil {
		t.Error("a read-only ingress started an archive poller (it never sends)")
	}
}

// Close is safe for concurrent callers and idempotent (review #387 P3-1, #388 P3-2): N goroutines call it
// at once on a wiring that runs the archive poller; none panics, the poller has stopped afterwards and a
// later Close is a no-op.
func TestLifecycleCloseIsSafeForConcurrentCallers(t *testing.T) {
	dir := newOverlayDir(t)
	env, _ := lcEnv(t, dir)
	holder, err := openLifecycle(env, os.Geteuid(), false)
	if err != nil || holder == nil {
		t.Fatalf("holder: %v %v", holder, err)
	}
	t.Cleanup(holder.Close)
	second, err := openLifecycle(env, os.Geteuid(), false)
	if err != nil || second == nil || second.store.CanWrite() {
		t.Fatalf("second writer: %v %v", second, err)
	}
	newLCStack(t, second, false) // applies the wiring: the poller runs
	if second.stop == nil {
		t.Fatal("the poller did not start: this test would pin nothing")
	}
	const callers = 16
	start := make(chan struct{})
	var wg sync.WaitGroup
	panics := make(chan any, callers)
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() {
				if p := recover(); p != nil {
					panics <- p
				}
			}()
			<-start
			second.Close()
		}()
	}
	close(start)
	wg.Wait()
	close(panics)
	for p := range panics {
		t.Fatalf("a concurrent Close panicked: %v", p)
	}
	select {
	case <-second.done:
	default:
		t.Fatal("the poller is still running after Close")
	}
	second.Close() // idempotent
}

// A reader that never had a good snapshot refuses nothing, and adopts the file once it is repaired
// (ADR-025 4.4: "none exists at startup", an unknown state never blocks unrelated prompts; review #387
// P3-2). The lock holder archives sesA, the overlay file is replaced by garbage, a second process starts
// on it (state unknown, empty snapshot), then the good file comes back.
func TestLifecycleReaderWithoutAGoodSnapshotRefusesNothingThenAdoptsTheRepairedFile(t *testing.T) {
	dir := newOverlayDir(t)
	env, _ := lcEnv(t, dir)
	holder, err := openLifecycle(env, os.Geteuid(), false)
	if err != nil || holder == nil {
		t.Fatalf("holder: %v %v", holder, err)
	}
	t.Cleanup(holder.Close)
	archiveViaStore(t, holder, sesA)
	file := filepath.Join(dir, "overlay.json")
	good, err := os.ReadFile(file)
	if err != nil || len(good) == 0 {
		t.Fatalf("the holder's overlay file: %v (%d bytes)", err, len(good))
	}
	replace := func(content []byte) { // a new inode each time, so the stat poll sees it
		tmp := file + ".tmp"
		if err := os.WriteFile(tmp, content, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(tmp, file); err != nil {
			t.Fatal(err)
		}
	}
	replace([]byte("not json"))
	second, err := openLifecycle(env, os.Geteuid(), false)
	if err != nil || second == nil || second.store.CanWrite() {
		t.Fatalf("second process: %v %v", second, err)
	}
	t.Cleanup(second.Close)
	if second.store.Err() == nil {
		t.Fatal("the second process did not see the file as invalid: this test would pin nothing")
	}
	st := newLCStack(t, second, false)
	if err := st.send(sesA); archivedRefusal(err) {
		t.Fatalf("a reader with no good snapshot refused a prompt: %v", err)
	}
	if err := st.send(sesB); archivedRefusal(err) {
		t.Fatalf("a reader with no good snapshot refused an unrelated prompt: %v", err)
	}
	replace(good)
	for end := time.Now().Add(5 * time.Second); !archivedRefusal(st.send(sesA)); time.Sleep(100 * time.Millisecond) {
		if time.Now().After(end) {
			t.Fatal("the repaired file was never adopted")
		}
	}
	if err := st.send(sesB); archivedRefusal(err) {
		t.Fatalf("an unrelated session was refused after the repair: %v", err)
	}
}
