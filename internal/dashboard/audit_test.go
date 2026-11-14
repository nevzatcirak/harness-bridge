package dashboard

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"
)

// recordingHandler is a journal stand-in that records what it was asked to write.
type recordingHandler struct {
	mu      *sync.Mutex
	lines   *[]string
	level   slog.Level
	attrs   []slog.Attr
	groups  []string
	handled *int
}

func newRecording(level slog.Level) *recordingHandler {
	return &recordingHandler{mu: &sync.Mutex{}, lines: &[]string{}, level: level, handled: new(int)}
}

func (h *recordingHandler) Enabled(_ context.Context, l slog.Level) bool { return l >= h.level }
func (h *recordingHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	*h.handled++
	var b strings.Builder
	b.WriteString(r.Level.String() + " " + r.Message)
	for _, a := range h.attrs {
		fmt.Fprintf(&b, " %s=%v", a.Key, a.Value)
	}
	r.Attrs(func(a slog.Attr) bool { fmt.Fprintf(&b, " %s=%v", a.Key, a.Value); return true })
	*h.lines = append(*h.lines, b.String())
	return nil
}
func (h *recordingHandler) WithAttrs(a []slog.Attr) slog.Handler {
	c := *h
	c.attrs = append(append([]slog.Attr(nil), h.attrs...), a...)
	return &c
}
func (h *recordingHandler) WithGroup(n string) slog.Handler {
	c := *h
	c.groups = append(append([]string(nil), h.groups...), n)
	return &c
}

func teeLogger(level slog.Level) (*slog.Logger, *AuditRing, *recordingHandler) {
	j := newRecording(level)
	ring := NewAuditRing()
	return slog.New(NewAuditTee(j, ring)), ring, j
}

func TestRingIsExactly1000DropOldestNewestFirst(t *testing.T) {
	l, ring, _ := teeLogger(slog.LevelInfo)
	if len(ring.Entries()) != 0 {
		t.Fatal("a new ring is not empty")
	}
	for i := 0; i < AuditRingSize+250; i++ {
		l.Info("explicit interrupt", "event", "interrupt", "tool", "interrupt_execution", "execution_id", fmt.Sprintf("n%04d", i))
	}
	got := ring.Entries()
	if AuditRingSize != 1000 || len(got) != 1000 {
		t.Fatalf("ring holds %d entries, want exactly 1000", len(got))
	}
	if got[0].Attr("execution_id") != "n1249" || got[999].Attr("execution_id") != "n0250" {
		t.Fatalf("newest first / drop-oldest broken: first %q last %q", got[0].Attr("execution_id"), got[999].Attr("execution_id"))
	}
	for i := 1; i < len(got); i++ {
		if got[i].Attr("execution_id") >= got[i-1].Attr("execution_id") {
			t.Fatalf("not newest first at %d", i)
		}
	}
	// Entries is a copy: changing it does not change the ring.
	got[0].Attrs[0].Value = "tampered"
	got[0].Message = "tampered"
	if again := ring.Entries(); again[0].Message == "tampered" || again[0].Attrs[0].Value == "tampered" {
		t.Fatal("Entries returned an alias of the ring")
	}
}

func TestRingKeepsOnlyAllowlistedEventsAndAttributes(t *testing.T) {
	l, ring, journal := teeLogger(slog.LevelInfo)
	l.Info("prompt submission", "event", "prompt_submission", "tool", "send_prompt", "outcome", "new", "accepted", true,
		"session_id", "opencode:ses_a", "execution_id", "abc", "idem", "0123456789ab", "principal", "local",
		"prompt", "TOP SECRET PROMPT", "authorization", "Bearer abcdefghijklmnop", "token_x", "sk-ant-aaaaaaaaaaaaaaaa",
		"settle_ms", int64(250), "ticket", "T-1")
	l.Info("guard release", "event", "guard_release", "tool", "admin", "outcome", "refused", "accepted", false, "error_code", "INVALID_INPUT",
		"native_state", "idle", "unfinished", false, "permission_pending", false, "correlated_user", true,
		"allow_missing_user_message", true, "principal", "admin:uid=996")
	// Not allowlisted events, and records with no event at all, are dropped (but still journaled).
	l.Info("remote boundary", "principal", "p", "boundary_allowed", true)
	l.Info("lifecycle", "event", "create_session", "outcome", "created")
	l.Info("future", "event", "permission_reply", "outcome", "x")
	l.Info("noise", "event", 42, "outcome", "x")
	l.Info("ev", "event", slog.GroupValue(slog.String("event", "interrupt")))
	l.Warn("warn with event", "event", "interrupt", "tool", "x") // an allowlisted event at another level is kept
	got := ring.Entries()
	if len(got) != 3 {
		t.Fatalf("%d entries kept, want 3 (two Info, one Warn): %+v", len(got), got)
	}
	var wantKeys = map[string]bool{}
	for k := range auditAttrs {
		wantKeys[k] = true
	}
	for _, e := range got {
		for _, a := range e.Attrs {
			if !wantKeys[a.Key] {
				t.Errorf("attribute %q reached the ring", a.Key)
			}
			if strings.Contains(a.Value, "SECRET") || strings.Contains(a.Value, "Bearer abc") || strings.Contains(a.Value, "sk-ant-aaaa") {
				t.Errorf("sensitive value %q reached the ring", a.Value)
			}
		}
	}
	pe := got[2] // oldest = the prompt_submission
	if pe.Event != "prompt_submission" || pe.Attr("settle_ms") != "250" || pe.Attr("accepted") != "true" || pe.Attr("prompt") != "" || pe.Attr("authorization") != "" || pe.Attr("token_x") != "" {
		t.Errorf("projection of the prompt line: %+v", pe)
	}
	// Order is the writer's order.
	var keys []string
	for _, a := range pe.Attrs {
		keys = append(keys, a.Key)
	}
	if strings.Join(keys, ",") != "event,tool,outcome,accepted,session_id,execution_id,idem,principal,settle_ms,ticket" {
		t.Errorf("attribute order %v", keys)
	}
	ge := got[1]
	if ge.Event != "guard_release" || ge.Attr("error_code") != "INVALID_INPUT" || ge.Attr("principal") != "admin:uid=996" || ge.Attr("correlated_user") != "true" {
		t.Errorf("projection of the guard_release line: %+v", ge)
	}
	// The journal saw every record, untouched.
	if *journal.handled != 8 {
		t.Errorf("the journal handled %d records, want 8", *journal.handled)
	}
}

// No attribute can be smuggled through a group or a pre-bound logger.
func TestAllowlistHoldsForWithAttrsAndWithGroup(t *testing.T) {
	l, ring, _ := teeLogger(slog.LevelInfo)
	// A pre-bound allowlisted attribute is kept; a pre-bound unknown one is not.
	bound := l.With("event", "interrupt", "principal", "local", "prompt", "SECRET-BOUND", "sk_key", "sk-ant-bbbbbbbbbbbbbbbb")
	bound.Info("explicit interrupt", "tool", "interrupt_execution", "x_extra", "SECRET-REC")
	// After WithGroup every attribute is namespaced: none is allowlisted, even one named like an allowlisted key.
	g := bound.WithGroup("g")
	g.Info("grouped", "event", "guard_release", "tool", "admin", "outcome", "hidden", "x_extra", "SECRET-GROUP")
	// An attribute bound after the group is also namespaced.
	g2 := l.WithGroup("g2").With("event", "interrupt")
	g2.Info("grouped-bound")
	// An allowlisted key inside a Group value is not a top-level attribute.
	l.Info("group value", "event", "interrupt", slog.Group("tool", slog.String("event", "x"), slog.String("outcome", "SECRET-IN-GROUP")))
	l.Info("empty-key group", slog.Group("", slog.String("event", "interrupt")))
	got := ring.Entries()
	if len(got) != 3 {
		t.Fatalf("%d entries, want 3 (the pre-bound interrupt, the one already bound before the group, and the group-value one): %+v", len(got), got)
	}
	for _, e := range got {
		for _, a := range e.Attrs {
			if strings.Contains(a.Value, "SECRET") || strings.Contains(a.Value, "sk-ant") || a.Key == "prompt" || a.Key == "x_extra" || a.Key == "sk_key" {
				t.Errorf("smuggled attribute %+v in %+v", a, e)
			}
		}
	}
	// The record bound before the group keeps the pre-bound event and principal.
	var bound1 *AuditEntry
	for i := range got {
		if got[i].Attr("principal") == "local" {
			bound1 = &got[i]
		}
	}
	if bound1 == nil || bound1.Event != "interrupt" || bound1.Attr("tool") != "interrupt_execution" {
		t.Errorf("the pre-bound allowlisted attributes were lost: %+v", got)
	}
	for _, e := range got {
		if e.Attr("outcome") == "hidden" {
			t.Errorf("a grouped record reached the ring: %+v", e)
		}
	}
}

// A secret-looking value under an allowlisted key is redacted and bounded; control
// characters and invalid UTF-8 are cleaned.
func TestRingValuesAreCleanedRedactedAndBounded(t *testing.T) {
	l, ring, _ := teeLogger(slog.LevelInfo)
	l.Info("interrupt\nwith newline", "event", "interrupt", "session_id", "opencode:ses_a ghp_"+strings.Repeat("a", 30),
		"principal", "evil\x00\x1b[31mred\xff", "error_code", strings.Repeat("é", 400), "outcome", "Authorization: Bearer abcdefghijklmnop")
	e := ring.Entries()[0]
	if strings.Contains(e.Attr("session_id"), "ghp_aaa") || !strings.Contains(e.Attr("session_id"), "[redacted]") {
		t.Errorf("session_id %q", e.Attr("session_id"))
	}
	if p := e.Attr("principal"); strings.ContainsRune(p, 0) || strings.ContainsRune(p, 0x1b) || !utf8.ValidString(p) || strings.ContainsRune(e.Message, '\n') {
		t.Errorf("control or invalid bytes kept: %q %q", e.Attr("principal"), e.Message)
	}
	if v := e.Attr("error_code"); len(v) > maxAuditValue+len("…") || !strings.HasSuffix(v, "…") {
		t.Errorf("value not bounded: %d bytes", len(v))
	}
	if strings.Contains(e.Attr("outcome"), "abcdefghijklmnop") {
		t.Errorf("a bearer token reached the ring: %q", e.Attr("outcome"))
	}
}

func TestTeeJournalBranchIsCalledOncePerEnabledRecordAndNeverForDisabledOnes(t *testing.T) {
	l, ring, j := teeLogger(slog.LevelInfo)
	l.Debug("debug", "event", "interrupt")
	l.Info("info", "event", "interrupt")
	l.Error("err", "event", "interrupt")
	if *j.handled != 2 {
		t.Errorf("journal handled %d, want 2 (Info and Error; Debug is below its level)", *j.handled)
	}
	if n := len(ring.Entries()); n != 2 {
		t.Errorf("ring holds %d, want 2: a debug record is not an audit line", n)
	}
	// With-bound attributes reach the journal exactly as logged.
	b := l.With("a", 1)
	b.Info("x", "event", "interrupt")
	last := (*j.lines)[len(*j.lines)-1]
	if last != "INFO x a=1 event=interrupt" {
		t.Errorf("journal line %q", last)
	}
}

func TestRingIsSafeForConcurrentWriters(t *testing.T) {
	l, ring, _ := teeLogger(slog.LevelInfo)
	var wg sync.WaitGroup
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < 400; i++ {
				l.Info("explicit interrupt", "event", "interrupt", "execution_id", fmt.Sprintf("w%d-%d", w, i))
				if i%50 == 0 {
					_ = ring.Entries()
				}
			}
		}(w)
	}
	wg.Wait()
	if n := len(ring.Entries()); n != AuditRingSize {
		t.Fatalf("%d entries after 3200 writes", n)
	}
}

// A panicking LogValuer must not fail the log call.
type panicValuer struct{}

func (panicValuer) LogValue() slog.Value { panic("boom") }

func TestRingBranchNeverFailsALogCall(t *testing.T) {
	l, ring, j := teeLogger(slog.LevelInfo)
	l.Info("x", "event", "interrupt", "tool", panicValuer{})
	if *j.handled != 1 {
		t.Fatal("the journal did not get the record")
	}
	_ = ring.Entries()
}

// The golden test over the REAL default handler: the journal bytes with the tee
// installed equal the bytes without it, including a record the ring drops and a group.
func journalBytes(t *testing.T, install bool, emit func()) string {
	t.Helper()
	var buf bytes.Buffer
	prevW, prevF, prevD := log.Writer(), log.Flags(), slog.Default()
	log.SetOutput(&buf)
	log.SetFlags(0)
	slog.SetDefault(prevD) // a default handler writes through the log package
	defer func() { log.SetOutput(prevW); log.SetFlags(prevF); slog.SetDefault(prevD) }()
	if install {
		ring := NewAuditRing()
		restore := InstallAuditTee(ring)
		defer restore()
	}
	emit()
	return buf.String()
}

func TestJournalBytesAreIdenticalWithAndWithoutTheTee(t *testing.T) {
	emit := func() {
		slog.Info("prompt submission", "event", "prompt_submission", "tool", "send_prompt", "outcome", "new", "accepted", true, "session_id", "opencode:ses_a", "execution_id", "abc123", "idem", "0123456789ab", "principal", "local")
		slog.Info("explicit interrupt", "event", "interrupt", "tool", "interrupt_execution", "outcome", "accepted", "accepted", true, "principal", "local")
		slog.Info("remote authentication", "principal", "ops", "accepted", false)
		slog.Info("secret-looking", "event", "prompt_submission", "x", "sk-ant-cccccccccccccccc", slog.Group("g", slog.Int("n", 3)))
		slog.With("event", "guard_release").WithGroup("grp").Warn("grouped", "k", "v")
		slog.Debug("below the default level", "event", "interrupt")
		log.Printf("a plain log line %d", 7)
	}
	without := journalBytes(t, false, emit)
	with := journalBytes(t, true, emit)
	if without == "" || without != with {
		t.Fatalf("journal output changed by the tee\n--- without:\n%s\n--- with:\n%s", without, with)
	}
	if strings.Count(with, "\n") != 6 {
		t.Errorf("journal has %d lines, want 6 (debug is not written)", strings.Count(with, "\n"))
	}
}

// Installing the tee must not loop (the default handler writes through the log package)
// and must put everything back on restore.
func TestInstallAuditTeeDoesNotLoopAndRestores(t *testing.T) {
	var buf bytes.Buffer
	prevW, prevF, prevD := log.Writer(), log.Flags(), slog.Default()
	log.SetOutput(&buf)
	log.SetFlags(0)
	defer func() { log.SetOutput(prevW); log.SetFlags(prevF); slog.SetDefault(prevD) }()
	ring := NewAuditRing()
	restore := InstallAuditTee(ring)
	done := make(chan struct{})
	go func() {
		slog.Info("explicit interrupt", "event", "interrupt")
		log.Printf("hello")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("the tee loops through the log package")
	}
	if strings.Count(buf.String(), "\n") != 2 || len(ring.Entries()) != 1 {
		t.Fatalf("journal %q ring %d", buf.String(), len(ring.Entries()))
	}
	restore()
	if slog.Default() != prevD || log.Flags() != 0 {
		t.Fatal("restore did not put the logger back")
	}
	slog.Info("after restore", "event", "interrupt")
	if len(ring.Entries()) != 1 {
		t.Fatal("the ring is still fed after restore")
	}
}

// The audit pages (ADR-023 section 5.3).
type fakeAudit struct {
	entries []AuditEntry
	calls   int
	block   chan struct{}
	entered chan struct{}
	mu      sync.Mutex
}

func (f *fakeAudit) Entries() []AuditEntry {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	if f.entered != nil {
		select {
		case f.entered <- struct{}{}:
		default:
		}
	}
	if f.block != nil {
		<-f.block
	}
	return f.entries
}

func sampleAudit() []AuditEntry {
	return []AuditEntry{
		{Time: t0, Message: "explicit interrupt", Event: "interrupt", Attrs: []AuditAttr{{"event", "interrupt"}, {"outcome", "accepted"}, {"execution_id", "72a76b2b"}, {"principal", "operator"}, {"tool", "interrupt_execution"}}},
		{Time: t0, Message: "guard release", Event: "guard_release", Attrs: []AuditAttr{{"event", "guard_release"}, {"outcome", "refused"}, {"error_code", "INVALID_INPUT"}, {"principal", "admin:uid=996"}}},
	}
}

func TestAuditPageShowsTheRingEscapedWithAClientSideFilter(t *testing.T) {
	hostile := sampleAudit()
	hostile[0].Attrs = append(hostile[0].Attrs, AuditAttr{"session_id", `<script>alert(1)</script>"><img src=x onerror=alert(2)>`})
	hostile[0].Message = `</title><script>alert(3)</script>`
	h := NewHandlerOptions(NullSource{}, nil, Options{Audit: &fakeAudit{entries: hostile}})
	code, body, hdr := fetch(h, "/audit")
	if code != http.StatusOK {
		t.Fatalf("GET /audit = %d", code)
	}
	checkSecurityHeaders(t, "GET /audit", hdr)
	for _, want := range []string{"interrupt", "accepted", "72a76b2b", "operator", "guard_release", "refused", "INVALID_INPUT", "admin:uid=996", `id="audit-filter"`, `id="audit-rows"`, "2 entries"} {
		if !strings.Contains(body, want) {
			t.Errorf("audit page lacks %q", want)
		}
	}
	assertNoActiveContent(t, "audit page", body)
	if strings.Contains(body, "<form") {
		t.Error("the audit page has a form")
	}
	// Newest first, as the source gave it: the interrupt row is before the guard_release row.
	if strings.Index(body, ">interrupt<") > strings.Index(body, ">guard_release<") {
		t.Error("rows are not in the source order")
	}
}

func TestAuditPageWithoutAFeedSaysSo(t *testing.T) {
	for _, h := range []http.Handler{NewHandler(NullSource{}, nil), NewHandlerOptions(NullSource{}, nil, Options{})} {
		code, body, _ := fetch(h, "/audit")
		if code != http.StatusOK || !strings.Contains(body, "audit feed is not enabled") || strings.Contains(body, "audit-rows") {
			t.Errorf("no feed: %d %s", code, body)
		}
		code, body, _ = fetch(h, "/api/audit")
		if code != http.StatusOK || !strings.Contains(body, `"enabled":false`) || !strings.Contains(body, `"entries":[]`) {
			t.Errorf("no feed JSON: %d %s", code, body)
		}
	}
}

func TestAuditAPIIsJSONWithETagAndNoFreeText(t *testing.T) {
	src := &fakeAudit{entries: sampleAudit()}
	h := NewHandlerOptions(NullSource{}, nil, Options{Audit: src})
	rec := serve(h, http.MethodGet, "/api/audit", "")
	if rec.Code != http.StatusOK || !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") || rec.Header().Get("ETag") == "" {
		t.Fatalf("GET /api/audit = %d %v", rec.Code, rec.Header())
	}
	checkSecurityHeaders(t, "GET /api/audit", rec.Header())
	for _, want := range []string{`"enabled":true`, `"limit":1000`, `"event":"interrupt"`, `"key":"outcome","value":"accepted"`} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("JSON lacks %s: %s", want, rec.Body.String())
		}
	}
	// The JSON carries only the closed entry fields: time, message, event, attrs.
	for _, banned := range []string{"prompt", "idempotency_key", "password", "token"} {
		if strings.Contains(strings.ToLower(rec.Body.String()), banned) {
			t.Errorf("%q in the audit JSON", banned)
		}
	}
	// A conditional request with the same ETag is 304.
	req := newGet("/api/audit")
	req.Header.Set("If-None-Match", rec.Header().Get("ETag"))
	rec2 := serveReq(h, req)
	if rec2.Code != http.StatusNotModified || rec2.Body.Len() != 0 {
		t.Errorf("If-None-Match = %d %q", rec2.Code, rec2.Body.String())
	}
}

// Dashboard traffic must never produce an audit entry or a journal line: it has no
// write path and the ring is written by the tee only.
func TestDashboardTrafficProducesNoAuditEntries(t *testing.T) {
	var buf bytes.Buffer
	prevW, prevF, prevD := log.Writer(), log.Flags(), slog.Default()
	log.SetOutput(&buf)
	log.SetFlags(0)
	defer func() { log.SetOutput(prevW); log.SetFlags(prevF); slog.SetDefault(prevD) }()
	ring := NewAuditRing()
	restore := InstallAuditTee(ring)
	defer restore()
	src := diffSource(t)
	src.rows = sampleRows()
	h := NewHandlerOptions(src, nil, Options{Audit: ring})
	paths := []string{"/", "/audit", "/api/audit", "/api/state", "/api/state/session/opencode/" + sesID, "/session/opencode/" + sesID,
		"/session/opencode/" + sesID + "/turn/" + turnX, "/dashboard.js", "/dashboard.css", "/nope"}
	for _, p := range paths {
		for _, m := range []string{http.MethodGet, http.MethodHead, http.MethodPost, http.MethodDelete} {
			serve(h, m, p, "")
		}
	}
	if n := len(ring.Entries()); n != 0 {
		t.Errorf("dashboard traffic produced %d audit entries", n)
	}
	if strings.Contains(buf.String(), "prompt_submission") || strings.Contains(buf.String(), "guard_release") || strings.Contains(buf.String(), "event=interrupt") {
		t.Errorf("dashboard traffic wrote an audit line: %s", buf.String())
	}
}

func TestDashboardGoHasNoWritePathToTheJournal(t *testing.T) {
	// The ring is written only by the tee's unexported add; the dashboard files that
	// serve requests never call it, and no file opens the journal.
	for _, f := range []string{"views.go", "handler.go", "poll.go", "auditview.go", "limits.go", "server.go", "source.go"} {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, banned := range []string{".add(", "os.OpenFile", "os.Create", "journalctl", "/var/log", "ioutil.WriteFile", "os.WriteFile"} {
			if strings.Contains(string(b), banned) {
				t.Errorf("%s contains %q", f, banned)
			}
		}
	}
}

func TestAuditSourceInterfaceIsReadOnly(t *testing.T) {
	// The only method the dashboard can call on the feed is Entries.
	var _ AuditSource = (*AuditRing)(nil)
	var _ AuditSource = (*fakeAudit)(nil)
	src, err := os.ReadFile("audit.go")
	if err != nil {
		t.Fatal(err)
	}
	i := strings.Index(string(src), "type AuditSource interface {")
	j := strings.Index(string(src)[i:], "\n}")
	body := string(src)[i : i+j]
	methods := 0
	for _, l := range strings.Split(body, "\n")[1:] {
		if strings.TrimSpace(l) != "" && !strings.HasPrefix(strings.TrimSpace(l), "//") {
			methods++
			if !strings.Contains(l, "Entries()") {
				t.Errorf("AuditSource has a method other than Entries: %q", l)
			}
		}
	}
	if methods != 1 {
		t.Errorf("AuditSource has %d methods, want exactly 1", methods)
	}
	_ = errors.New
}
