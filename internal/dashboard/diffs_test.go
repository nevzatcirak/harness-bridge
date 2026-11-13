package dashboard

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	sesID = "ses_diffs1"
	turnX = "msg_user_changes"
)

// fixtureDiff converts a native fixture (derived from the upstream source at
// 148842e, NOT captured live: UNVERIFIED) into the dashboard's DiffResult.
func fixtureDiff(t *testing.T, name string) DiffResult {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "adapters", "opencode", "testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	var items []struct {
		File      *string `json:"file"`
		Patch     *string `json:"patch"`
		Additions float64 `json:"additions"`
		Deletions float64 `json:"deletions"`
		Status    string  `json:"status"`
	}
	if err := json.Unmarshal(b, &items); err != nil {
		t.Fatal(err)
	}
	var out DiffResult
	for _, it := range items {
		f := DiffFile{Status: it.Status, Additions: it.Additions, Deletions: it.Deletions}
		if it.File != nil {
			f.Path, f.HasPath = *it.File, true
		}
		if it.Patch != nil {
			f.Patch, f.HasPatch = *it.Patch, true
		}
		out.Files = append(out.Files, f)
	}
	return out
}

func session(h, id string) SessionDetail {
	return SessionDetail{Session: Session{Harness: h, ID: id, Title: "t", Root: "/r", State: "idle", LastActivity: t0}}
}

func diffSource(t *testing.T) *fakeSource {
	return &fakeSource{
		details: map[string]SessionDetail{"opencode/" + sesID: session("opencode", sesID), "claudecode/4e59c1bd-4333-4a71-875d-cba9301bd4a9": session("claudecode", "4e59c1bd-4333-4a71-875d-cba9301bd4a9")},
		turns: []Turn{
			{MessageID: "msg_user_notcomputed", Created: t0, State: TurnNotComputed},
			{MessageID: "msg_user_nodiffskey", Created: t0, State: TurnNotComputed},
			{MessageID: "msg_user_empty", Created: t0, State: TurnComputedEmpty},
			{MessageID: turnX, Created: t0, State: TurnChanges},
		},
		turnDiffs: map[string]DiffResult{turnX: fixtureDiff(t, "session_diff_148842e.json")},
		workDiff:  fixtureDiff(t, "vcs_diff_148842e.json"),
	}
}

func labelOf(body string) string {
	for _, l := range []string{LabelUnavailable, LabelNotComputed, LabelComputedEmpty, LabelChanges} {
		if strings.Contains(body, `">`+l+`</span>`) {
			return l
		}
	}
	return ""
}

// The four labels, one per fixture state. "Not computed" is never "no changes".
func TestFourLabelsOnTheTurnPage(t *testing.T) {
	src := diffSource(t)
	h := NewHandler(src, nil)
	base := "/session/opencode/" + sesID + "/turn/"
	for _, tc := range []struct {
		turn, label string
		diffCalls   int
	}{
		{"msg_user_notcomputed", LabelNotComputed, 0},
		{"msg_user_nodiffskey", LabelNotComputed, 0}, // summary present without diffs
		{"msg_user_empty", LabelComputedEmpty, 0},
		{turnX, LabelChanges, 1},
	} {
		src.turnDiffCall = 0
		code, body, hdr := fetch(h, base+tc.turn)
		if code != http.StatusOK {
			t.Fatalf("%s = %d", tc.turn, code)
		}
		checkSecurityHeaders(t, tc.turn, hdr)
		if got := labelOf(body); got != tc.label {
			t.Errorf("%s: label %q, want %q\n%s", tc.turn, got, tc.label, body)
		}
		if tc.label == LabelNotComputed && strings.Contains(body, "no changes") {
			t.Errorf("%s: a not-computed turn is rendered as no changes", tc.turn)
		}
		if src.turnDiffCall != tc.diffCalls {
			t.Errorf("%s: %d native diff reads, want %d (only a turn with changes is read)", tc.turn, src.turnDiffCall, tc.diffCalls)
		}
	}
}

func TestTurnWithChangesShowsFilesFromTheFixture(t *testing.T) {
	_, body, _ := fetch(NewHandler(diffSource(t), nil), "/session/opencode/"+sesID+"/turn/"+turnX)
	for _, want := range []string{"h4/NOTE.md", "MARK-ee3f40", "src/new.go", "old.txt", "no patch in the answer", "(no file name)", "added", "deleted", "modified"} {
		if !strings.Contains(body, want) {
			t.Errorf("turn page lacks %q", want)
		}
	}
}

func TestWorkingTreeIsTheDefaultAndNeverSaysNoChangesForAnEmptyAnswer(t *testing.T) {
	src := diffSource(t)
	h := NewHandler(src, nil)
	code, body, _ := fetch(h, "/session/opencode/"+sesID)
	if code != http.StatusOK || labelOf(body) == "" || !strings.Contains(body, "working tree (not per turn)") {
		t.Fatalf("detail = %d\n%s", code, body)
	}
	for _, want := range []string{"README.md", "assets/logo.png", "binary file, not displayed", "patch not provided (OpenCode replaces a patch over its own size cap by a header)", "msg_user_changes", "/session/opencode/" + sesID + "/turn/" + turnX} {
		if !strings.Contains(body, want) {
			t.Errorf("detail lacks %q", want)
		}
	}
	// An empty array is ambiguous upstream (non-git, default branch): say so.
	src2 := diffSource(t)
	src2.workDiff = DiffResult{}
	_, body, _ = fetch(NewHandler(src2, nil), "/session/opencode/"+sesID)
	if !strings.Contains(body, "also what OpenCode returns for a non-git project") {
		t.Errorf("an empty working-tree answer must carry its caveat:\n%s", body)
	}
	// The turn list never links a turn without changes.
	if strings.Contains(body, "/turn/msg_user_notcomputed") || strings.Contains(body, "/turn/msg_user_empty") {
		t.Errorf("a turn without changes is linked")
	}
}

func TestClaudeCodeSessionsShowDiffUnavailableAdr021Pending(t *testing.T) {
	src := diffSource(t)
	id := "4e59c1bd-4333-4a71-875d-cba9301bd4a9"
	h := NewHandler(src, nil)
	_, body, _ := fetch(h, "/session/claudecode/"+id)
	if !strings.Contains(body, "diff unavailable (ADR-021 pending)") {
		t.Errorf("detail:\n%s", body)
	}
	code, body, _ := fetch(h, "/session/claudecode/"+id+"/turn/msg_x")
	if code != http.StatusOK || !strings.Contains(body, "diff unavailable (ADR-021 pending)") {
		t.Errorf("turn = %d\n%s", code, body)
	}
	if src.workCalls != 0 || src.turnsCalls != 0 || src.turnDiffCall != 0 {
		t.Errorf("a Claude Code session made native diff reads: %d %d %d", src.workCalls, src.turnsCalls, src.turnDiffCall)
	}
}

// T7/T10 side: a native diff that fails or hangs is "unavailable" and the page
// still renders within the 5 s bound.
func TestFailingOrSlowNativeDiffIsUnavailableAndThePageRenders(t *testing.T) {
	src := diffSource(t)
	src.workErr = errors.New("boom <b>secret</b>")
	src.turnsErr = context.DeadlineExceeded
	src.turnDiffErr = errors.New("native 500")
	h := NewHandler(src, nil)
	code, body, _ := fetch(h, "/session/opencode/"+sesID)
	if code != http.StatusOK || !strings.Contains(body, `">`+LabelUnavailable+`</span>`) || strings.Contains(body, "boom") || strings.Contains(body, "secret") {
		t.Fatalf("detail with failing reads = %d\n%s", code, body)
	}
	if !strings.Contains(body, "turn list unavailable (timeout)") {
		t.Errorf("a timed-out turn list must say so")
	}
	src.turnsErr = nil
	code, body, _ = fetch(h, "/session/opencode/"+sesID+"/turn/"+turnX)
	if code != http.StatusOK || labelOf(body) != LabelUnavailable || strings.Contains(body, "native 500") {
		t.Fatalf("turn with a failing read = %d %q\n%s", code, labelOf(body), body)
	}

	slow := diffSource(t)
	slow.block = func(ctx context.Context) { <-ctx.Done() } // a native read that never returns
	start := time.Now()
	code, body, _ = fetch(NewHandler(slow, nil), "/session/opencode/"+sesID)
	if el := time.Since(start); el > requestBound+2*time.Second {
		t.Fatalf("a hung native read held the page for %v", el)
	}
	if code != http.StatusOK || labelOf(body) != LabelUnavailable || !strings.Contains(body, "(timeout)") {
		t.Fatalf("slow detail = %d %q\n%s", code, labelOf(body), body)
	}
}

func TestEmptyAnswerForAReportedComputedTurnIsUnavailable(t *testing.T) {
	src := diffSource(t)
	src.turnDiffs[turnX] = DiffResult{}
	_, body, _ := fetch(NewHandler(src, nil), "/session/opencode/"+sesID+"/turn/"+turnX)
	if labelOf(body) != LabelUnavailable || strings.Contains(body, "no changes") {
		t.Errorf("an empty answer for a turn reported as changed must not read as no changes:\n%s", body)
	}
}

// Negative: unknown session / message ID is a fixed-text 404 that never echoes.
func TestTurnRefusalsAreFixedText404(t *testing.T) {
	src := diffSource(t)
	h := NewHandler(src, nil)
	for _, p := range []string{
		"/session/opencode/ses_unknown/turn/" + turnX,
		"/session/opencode/" + sesID + "/turn/msg_unknown",
		"/session/opencode/" + sesID + "/turn/",
		"/session/opencode/" + sesID + "/turn/..",
		"/session/opencode/" + sesID + "/turn/%3Cscript%3Ealert(1)%3C%2Fscript%3E",
		"/session/opencode/" + sesID + "/turn/msg_a/extra",
		"/session/opencode/" + sesID + "/turns/" + turnX,
		"/session/opencode/" + sesID + "/TURN/" + turnX,
	} {
		code, body, hdr := fetch(h, p)
		if code != http.StatusNotFound || body != notFoundText+"\n" {
			t.Errorf("GET %s = %d %q", p, code, body)
		}
		checkSecurityHeaders(t, p, hdr)
	}
	if src.turnDiffCall != 0 {
		t.Errorf("a refused request made %d native diff reads", src.turnDiffCall)
	}
	// An unlisted-session error from the diff read is the same 404.
	src.turnDiffs = map[string]DiffResult{}
	code, body, _ := fetch(h, "/session/opencode/"+sesID+"/turn/"+turnX)
	if code != http.StatusNotFound || body != notFoundText+"\n" {
		t.Errorf("unlisted diff = %d %q", code, body)
	}
}

// Caps: per-file bytes, total bytes, file count, binary; every omission is marked.
func TestCapsAndBinary(t *testing.T) {
	big := func(n int) string { return "Index: f\n@@ -1 +1 @@\n" + strings.Repeat("x", n) + "\n" }
	t.Run("oversize file", func(t *testing.T) {
		src := diffSource(t)
		src.workDiff = DiffResult{Files: []DiffFile{{Path: "big.txt", HasPath: true, Patch: big(maxPatchPerFile + 5000), HasPatch: true, Additions: 1, Status: "modified"}}}
		_, body, _ := fetch(NewHandler(src, nil), "/session/opencode/"+sesID)
		if !strings.Contains(body, fmt.Sprintf("truncated (per-file cap %d bytes", maxPatchPerFile)) {
			t.Errorf("an oversize file is not marked truncated")
		}
		if n := strings.Count(body, "xxxxxxxxxx"); n == 0 || len(body) > maxPatchPerFile+60000 {
			t.Errorf("an oversize file is not cut (body %d bytes)", len(body))
		}
	})
	t.Run("oversize total", func(t *testing.T) {
		src := diffSource(t)
		var files []DiffFile
		for i := 0; i < 8; i++ {
			files = append(files, DiffFile{Path: fmt.Sprintf("f%d.txt", i), HasPath: true, Patch: big(200 << 10), HasPatch: true, Additions: 1})
		}
		src.workDiff = DiffResult{Files: files}
		_, body, _ := fetch(NewHandler(src, nil), "/session/opencode/"+sesID)
		if !strings.Contains(body, fmt.Sprintf("not shown: total cap of %d bytes reached", maxPatchTotal)) && !strings.Contains(body, "total cap") {
			t.Errorf("the total cap is not marked")
		}
		if len(body) > maxPatchTotal+120000 {
			t.Errorf("total cap not applied: body %d bytes", len(body))
		}
	})
	t.Run("too many files", func(t *testing.T) {
		src := diffSource(t)
		var files []DiffFile
		for i := 0; i < maxDiffFiles+37; i++ {
			files = append(files, DiffFile{Path: fmt.Sprintf("n%03d.txt", i), HasPath: true, Patch: "@@ -1 +1 @@\n+a\n", HasPatch: true, Additions: 1})
		}
		src.workDiff = DiffResult{Files: files}
		_, body, _ := fetch(NewHandler(src, nil), "/session/opencode/"+sesID)
		if n := strings.Count(body, `<section class="file">`); n != maxDiffFiles {
			t.Errorf("%d files rendered, want %d", n, maxDiffFiles)
		}
		if !strings.Contains(body, "37 more files not shown") {
			t.Errorf("the file-count cap is not marked")
		}
	})
	t.Run("binary", func(t *testing.T) {
		src := diffSource(t)
		src.workDiff = DiffResult{Files: []DiffFile{
			{Path: "nul.bin", HasPath: true, Patch: "Index: x\n@@ -1 +1 @@\n+\x00\x01\x02", HasPatch: true, Additions: 1},
			{Path: "git.bin", HasPath: true, Patch: "Index: x\nBinary files a and b differ\n", HasPatch: true},
			{Path: "latin1.bin", HasPath: true, Patch: "@@ -1 +1 @@\n+" + strings.Repeat("\xff\xfe", 200), HasPatch: true, Additions: 1},
		}}
		_, body, _ := fetch(NewHandler(src, nil), "/session/opencode/"+sesID)
		if n := strings.Count(body, "binary file, not displayed"); n != 3 {
			t.Errorf("%d binary notices, want 3", n)
		}
		if strings.Contains(body, "\x00") || strings.Contains(body, "Binary files a and b") {
			t.Errorf("binary content reached the page")
		}
	})
}

// Redaction is ON by default and runs before rendering.
func TestRedactionIsOnByDefaultWithNoConfig(t *testing.T) {
	secrets := []string{
		"sk-ant-FAKE0123456789abcdef", "ghp_" + strings.Repeat("a1", 12), "github_pat_" + strings.Repeat("Ab1", 10),
		"AKIAABCDEFGHIJKLMNOP", "Authorization: Bearer abcdef1234567890", "-----BEGIN RSA PRIVATE KEY-----\nMIIBOgIBAAJBAL\n-----END RSA PRIVATE KEY-----",
	}
	src := diffSource(t)
	var patch strings.Builder
	patch.WriteString("Index: s\n@@ -0,0 +1,9 @@\n")
	for _, s := range secrets {
		patch.WriteString("+" + s + "\n")
	}
	src.workDiff = DiffResult{Files: []DiffFile{{Path: "leak-sk-ant-FAKE0123456789abcdef.txt", HasPath: true, Patch: patch.String(), HasPatch: true, Additions: 9}}}
	_, body, _ := fetch(NewHandler(src, nil), "/session/opencode/"+sesID)
	for _, s := range secrets {
		core := strings.Split(s, "\n")[0]
		if strings.Contains(body, core) {
			t.Errorf("secret %q reached the page", core)
		}
	}
	if strings.Contains(body, "MIIBOgIBAAJBAL") || strings.Contains(body, "abcdef1234567890") {
		t.Errorf("secret material reached the page")
	}
	if !strings.Contains(body, "[redacted]") || !strings.Contains(body, "redaction on: ") || strings.Contains(body, "redaction on: 0 values") {
		t.Errorf("redaction hits are not counted and shown:\n%s", body)
	}
}

func TestRedactionCoversATokenThatStraddlesTheCut(t *testing.T) {
	token := "sk-ant-" + strings.Repeat("Z9", 40)
	patch := "@@ -1 +1 @@\n+" + strings.Repeat("y", maxPatchPerFile-len("@@ -1 +1 @@\n+")-10) + token // only 10 bytes of the token fit before the cut: too short for a pattern to match by itself + "\n" + strings.Repeat("tail\n", 100)
	src := diffSource(t)
	src.workDiff = DiffResult{Files: []DiffFile{{Path: "cut.txt", HasPath: true, Patch: patch, HasPatch: true, Additions: 1}}}
	_, body, _ := fetch(NewHandler(src, nil), "/session/opencode/"+sesID)
	if strings.Contains(body, "sk-ant-") || strings.Contains(body, "Z9Z9Z9") {
		t.Errorf("a token that straddles the per-file cut leaked a prefix")
	}
}

func TestOperatorPatternsAddToTheDefaults(t *testing.T) {
	if _, err := NewRedactor([]string{"("}); err == nil {
		t.Error("an invalid operator pattern was accepted")
	}
	if _, err := NewRedactor(make([]string, maxExtraPatterns+1)); err == nil {
		t.Error("too many operator patterns were accepted")
	}
	if _, err := NewRedactor([]string{strings.Repeat("a", maxExtraPatternLen+1)}); err == nil {
		t.Error("an over-long operator pattern was accepted")
	}
	red, err := NewRedactor([]string{`corp-[0-9]{6}`})
	if err != nil {
		t.Fatal(err)
	}
	src := diffSource(t)
	src.workDiff = DiffResult{Files: []DiffFile{{Path: "a", HasPath: true, Patch: "@@ -1 +1 @@\n+corp-123456 and sk-ant-FAKE0123456789abcdef\n", HasPatch: true, Additions: 1}}}
	_, body, _ := fetch(NewHandlerOptions(src, nil, Options{Redactor: red}), "/session/opencode/"+sesID)
	if strings.Contains(body, "corp-123456") || strings.Contains(body, "sk-ant-") {
		t.Errorf("operator pattern and defaults must both apply")
	}
	if got, n := DefaultRedactor().Redact("plain text"); got != "plain text" || n != 0 {
		t.Errorf("default redactor changed plain text: %q %d", got, n)
	}
}

// XSS (T1): hostile file names and hunks are escaped.
func TestDiffEscapesHostileNamesAndHunks(t *testing.T) {
	src := diffSource(t)
	src.workDiff = DiffResult{Files: []DiffFile{{
		Path: `"><img src=x onerror=alert(1)>`, HasPath: true, Status: "<b>x</b>", Additions: 1,
		Patch: "@@ -1 +1 @@\n+</pre><script>alert(2)</script>\n", HasPatch: true,
	}}}
	src.turns = []Turn{{MessageID: turnX, Created: t0, State: TurnChanges}}
	src.turnDiffs[turnX] = src.workDiff
	for _, p := range []string{"/session/opencode/" + sesID, "/session/opencode/" + sesID + "/turn/" + turnX} {
		_, body, _ := fetch(NewHandler(src, nil), p)
		assertNoActiveContent(t, p, body)
		if strings.Contains(body, "</pre><script>") {
			t.Errorf("%s: hunk not escaped", p)
		}
	}
}

// Single-flight and the poll-interval cache (D17 Q2).
func TestDiffsAreCachedForThePollIntervalAndSingleFlight(t *testing.T) {
	now := time.Unix(1700000000, 0)
	var mu sync.Mutex
	clock := func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	src := diffSource(t)
	h := NewHandlerOptions(src, nil, Options{Now: clock})
	for i := 0; i < 3; i++ {
		fetch(h, "/session/opencode/"+sesID+"/turn/"+turnX)
	}
	if src.turnDiffCall != 1 {
		t.Fatalf("%d native turn-diff reads within one poll interval, want 1", src.turnDiffCall)
	}
	mu.Lock()
	now = now.Add(diffCacheTTL + time.Millisecond)
	mu.Unlock()
	fetch(h, "/session/opencode/"+sesID+"/turn/"+turnX)
	if src.turnDiffCall != 2 {
		t.Fatalf("%d reads after the interval, want 2", src.turnDiffCall)
	}
	// Concurrent requests share one read.
	src2 := diffSource(t)
	gate := make(chan struct{})
	var started sync.WaitGroup
	started.Add(1)
	var once sync.Once
	src2.blockTurnDiff = func(ctx context.Context) { once.Do(started.Done); <-gate }
	h2 := NewHandler(src2, nil)
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); fetch(h2, "/session/opencode/"+sesID+"/turn/"+turnX) }()
	}
	started.Wait()
	time.Sleep(50 * time.Millisecond)
	close(gate)
	wg.Wait()
	if src2.turnDiffCall != 1 {
		t.Fatalf("%d concurrent native reads for one diff, want 1 (single-flight)", src2.turnDiffCall)
	}
}

func TestDiffCacheIsBounded(t *testing.T) {
	c := newDiffCache(nil)
	for i := 0; i < diffCacheMax*3; i++ {
		_, _ = c.get(context.Background(), fmt.Sprint("k", i), func(context.Context) (DiffResult, error) { return DiffResult{}, nil })
	}
	if len(c.m) > diffCacheMax {
		t.Fatalf("the cache holds %d entries, bound %d", len(c.m), diffCacheMax)
	}
}

// No Bridge-side git: a session root with hostile git config must stay untouched
// by every dashboard route. Nothing here can run git: this is also checked by the
// import and selector guard below.
func TestNoGitIsRunForAHostileRoot(t *testing.T) {
	root := t.TempDir()
	canary := filepath.Join(root, "CANARY")
	script := filepath.Join(root, "hook.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\ntouch "+canary+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := "[core]\n\tfsmonitor = " + script + "\n[filter \"x\"]\n\tclean = " + script + "\n"
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".git", "config"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	src := diffSource(t)
	d := session("opencode", sesID)
	d.Root = root
	src.details["opencode/"+sesID] = d
	src.rows = []Session{d.Session}
	h := NewHandler(src, nil)
	for _, p := range []string{"/", "/session/opencode/" + sesID, "/session/opencode/" + sesID + "/turn/" + turnX, "/session/opencode/" + sesID + "/turn/msg_user_empty"} {
		fetch(h, p)
	}
	if _, err := os.Stat(canary); err == nil {
		t.Fatal("the canary was written: something ran git or the hook")
	}
}

func TestDashboardImportsNoProcessApi(t *testing.T) {
	banned := []string{`"os/exec"`, `"os/signal"`, `"plugin"`, `"unsafe"`, `"net/rpc"`}
	ents, _ := os.ReadDir(".")
	for _, e := range ents {
		name := e.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		b, _ := os.ReadFile(name)
		src := string(b)
		for _, bad := range banned {
			if strings.Contains(src, bad) {
				t.Errorf("%s imports %s", name, bad)
			}
		}
		for _, call := range []string{"exec.Command", "exec.CommandContext", "syscall.Exec", "syscall.ForkExec", "os.StartProcess"} {
			if strings.Contains(src, call) {
				t.Errorf("%s calls %s", name, call)
			}
		}
		if strings.Contains(src, `"git"`) {
			t.Errorf("%s names the git binary", name)
		}
	}
}
