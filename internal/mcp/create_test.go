package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/nevzatcirak/harness-bridge/internal/adapters/opencode"
	"github.com/nevzatcirak/harness-bridge/internal/execution"
	"github.com/nevzatcirak/harness-bridge/internal/lifecycle"
	"github.com/nevzatcirak/harness-bridge/internal/mcpclient"
	"github.com/nevzatcirak/harness-bridge/internal/security"
)

// ADR-022 slice S4: create_session against a FAKE native. Nothing here is live.

type nativeReq struct {
	method, path, directory, limit, body string
}

type createEnv struct {
	t          *testing.T
	s          *Server
	cc         *CreateConfig
	rootA      string // has its own .git: a create target with max_depth 0
	rootB      string
	mu         sync.Mutex
	reqs       []nativeReq
	listing    map[string][]opencode.Session // directory -> sessions the fake lists
	autoAdd    bool                          // a POST adds its session to the listing (stateful fake)
	nextID     int
	postHook   func(w http.ResponseWriter, r *http.Request, dir string) bool
	listHook   func(w http.ResponseWriter, r *http.Request) bool
	postStatus int
	clock      time.Time
}

func gitAt(t *testing.T, dir string) {
	t.Helper()
	for p, c := range map[string]string{filepath.Join(dir, ".git", "HEAD"): "ref: refs/heads/main\n", filepath.Join(dir, ".git", "config"): "[core]\n\tbare = false\n"} {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func newCreateEnv(t *testing.T, mod func(*lifecycle.Config)) *createEnv {
	t.Helper()
	canon := func(d string) string {
		r, err := filepath.EvalSymlinks(d)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	e := &createEnv{t: t, rootA: canon(t.TempDir()), rootB: canon(t.TempDir()), listing: map[string][]opencode.Session{}, nextID: 1, clock: time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)}
	gitAt(t, e.rootA)
	gitAt(t, e.rootB)
	old := createBound.Load()
	t.Cleanup(func() { createBound.Store(old) })
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		q := r.URL.Query()
		e.mu.Lock()
		e.reqs = append(e.reqs, nativeReq{r.Method, r.URL.Path, q.Get("directory"), q.Get("limit"), string(body)})
		listHook, postHook, postStatus := e.listHook, e.postHook, e.postStatus
		e.mu.Unlock()
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/session":
			if listHook != nil && listHook(w, r) {
				return
			}
			e.mu.Lock()
			list := append([]opencode.Session{}, e.listing[q.Get("directory")]...)
			e.mu.Unlock()
			_ = json.NewEncoder(w).Encode(list)
		case r.Method == http.MethodPost && r.URL.Path == "/session":
			if postHook != nil && postHook(w, r, q.Get("directory")) {
				return
			}
			if postStatus != 0 {
				w.WriteHeader(postStatus)
				return
			}
			var b struct {
				Title string `json:"title"`
			}
			_ = json.Unmarshal(body, &b)
			e.mu.Lock()
			id := fmt.Sprintf("ses_new%d", e.nextID)
			e.nextID++
			sess := opencode.Session{ID: id, Title: b.Title, Directory: q.Get("directory")}
			if e.autoAdd {
				e.listing[e.rootFor(sess.Directory)] = append(e.listing[e.rootFor(sess.Directory)], sess)
			}
			e.mu.Unlock()
			_ = json.NewEncoder(w).Encode(sess)
		default:
			t.Errorf("unexpected native request %s %s", r.Method, r.URL)
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(upstream.Close)
	client, err := opencode.New(upstream.URL, "", "")
	if err != nil {
		t.Fatal(err)
	}
	cfg := &lifecycle.Config{Roots: []lifecycle.Root{
		{Path: e.rootA, Modes: []string{lifecycle.ModeCreate}, MaxSessions: 3},
		{Path: e.rootB, Modes: []string{lifecycle.ModeCreate}, MaxSessions: 3, MaxDepth: 2},
	}, CreatePerHour: 5, ArchivePerHour: 5, MaxTotalSessions: 5}
	if mod != nil {
		mod(cfg)
	}
	cc, err := NewCreateConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	cc.now = func() time.Time { return e.clock }
	e.cc = cc
	e.s = &Server{Client: client, Create: cc}
	return e
}

func (e *createEnv) setPostHook(f func(http.ResponseWriter, *http.Request, string) bool) {
	e.mu.Lock()
	e.postHook = f
	e.mu.Unlock()
}

func (e *createEnv) setListHook(f func(http.ResponseWriter, *http.Request) bool) {
	e.mu.Lock()
	e.listHook = f
	e.mu.Unlock()
}

func (e *createEnv) setPostStatus(n int) {
	e.mu.Lock()
	e.postStatus = n
	e.mu.Unlock()
}

func (e *createEnv) rootFor(dir string) string {
	for _, r := range []string{e.rootA, e.rootB} {
		if within(r, dir) {
			return r
		}
	}
	return dir
}

func (e *createEnv) requests() []nativeReq {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]nativeReq(nil), e.reqs...)
}

func (e *createEnv) count(method string) int {
	n := 0
	for _, r := range e.requests() {
		if r.method == method {
			n++
		}
	}
	return n
}

func (e *createEnv) create(a args) (CreateOutput, error) {
	e.t.Helper()
	if a.IdempotencyKey == "" {
		a.IdempotencyKey = "key-" + fmt.Sprint(time.Now().UnixNano())
	}
	if err := validateArgs("create_session", a); err != nil {
		return CreateOutput{}, err
	}
	v, err := e.s.call(context.Background(), "create_session", a)
	if err != nil {
		return CreateOutput{}, err
	}
	return v.(CreateOutput), nil
}

func codeOf(err error) string {
	if err == nil {
		return ""
	}
	return envelope(err).Code
}

func (e *createEnv) mustCreate(a args) CreateOutput {
	e.t.Helper()
	out, err := e.create(a)
	if err != nil {
		e.t.Fatalf("create_session: %v", err)
	}
	return out
}

func (e *createEnv) mkdirs(parts ...string) string {
	p := filepath.Join(parts...)
	if err := os.MkdirAll(p, 0o755); err != nil {
		e.t.Fatal(err)
	}
	return p
}

func TestCreateSendsOnePostWithTheFullyResolvedPathAndTheFixedBody(t *testing.T) {
	e := newCreateEnv(t, nil)
	out := e.mustCreate(args{Root: e.rootA, Title: "hello", IdempotencyKey: "key-create-0001"})
	if out.SessionID != "opencode:ses_new1" || !out.Created || out.Root != e.rootA || out.Directory != e.rootA || out.Title != "hello" {
		t.Fatalf("%+v", out)
	}
	if e.count("POST") != 1 {
		t.Fatalf("%d POSTs", e.count("POST"))
	}
	var post nativeReq
	for _, r := range e.requests() {
		if r.method == "POST" {
			post = r
		}
	}
	if post.path != "/session" || post.directory != e.rootA {
		t.Fatalf("POST %+v", post)
	}
	// The body is exactly {title, metadata{hb_origin, hb_key}}: no permission, agent, model,
	// parentID, workspaceID, prompt or any other key.
	var body map[string]json.RawMessage
	if err := json.Unmarshal([]byte(post.body), &body); err != nil {
		t.Fatal(err)
	}
	keys := []string{}
	for k := range body {
		keys = append(keys, k)
	}
	if len(keys) != 2 || body["title"] == nil || body["metadata"] == nil {
		t.Fatalf("body keys %v: %s", keys, post.body)
	}
	var meta map[string]string
	_ = json.Unmarshal(body["metadata"], &meta)
	if len(meta) != 2 || meta["hb_origin"] != "bridge" || len(meta["hb_key"]) != 16 || strings.Contains(post.body, "key-create-0001") {
		t.Fatalf("metadata %v in %s (the raw key must never be sent)", meta, post.body)
	}
	// No title: the key is omitted, not sent empty.
	e2 := newCreateEnv(t, nil)
	e2.mustCreate(args{Root: e2.rootA, IdempotencyKey: "key-create-0002"})
	for _, r := range e2.requests() {
		if r.method == "POST" && strings.Contains(r.body, `"title"`) {
			t.Errorf("an empty title was sent: %s", r.body)
		}
	}
}

func TestCreateNeverSendsAnotherSpellingOfThePath(t *testing.T) {
	e := newCreateEnv(t, nil)
	sub := e.mkdirs(e.rootB, "p", "q")
	gitAt(t, e.rootB+"/p")
	out := e.mustCreate(args{Root: e.rootB, Subdirectory: "p/q", IdempotencyKey: "key-create-0003"})
	if out.Directory != sub {
		t.Fatalf("directory %q, want %q", out.Directory, sub)
	}
	for _, r := range e.requests() {
		if r.method == "POST" && r.directory != sub {
			t.Errorf("sent %q, want the canonical %q", r.directory, sub)
		}
	}
	// Other spellings of the same place are refused, never "fixed" and sent.
	for name, a := range map[string]args{
		"root with a trailing slash":     {Root: e.rootB + "/", Subdirectory: "p"},
		"root via ..":                    {Root: e.rootB + "/p/..", Subdirectory: "p"},
		"relative root":                  {Root: strings.TrimPrefix(e.rootB, "/"), Subdirectory: "p"},
		"subdirectory with ./":           {Root: e.rootB, Subdirectory: "./p"},
		"subdirectory with a double /":   {Root: e.rootB, Subdirectory: "p//q"},
		"subdirectory with a trailing /": {Root: e.rootB, Subdirectory: "p/"},
	} {
		before := len(e.requests())
		a.IdempotencyKey = "key-spell-" + strings.ReplaceAll(name, " ", "-")
		if _, err := e.create(a); err == nil {
			t.Errorf("%s: accepted", name)
		}
		if len(e.requests()) != before {
			t.Errorf("%s: a native call was made", name)
		}
	}
}

func TestCreateRefusesASymlinkedSubdirectoryWithoutAnyNativeCall(t *testing.T) {
	e := newCreateEnv(t, nil)
	outside := t.TempDir()
	gitAt(t, outside)
	e.mkdirs(e.rootB, "real")
	gitAt(t, e.rootB+"/real")
	if err := os.Symlink(outside, filepath.Join(e.rootB, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(e.rootB+"/real", filepath.Join(e.rootB, "alias")); err != nil {
		t.Fatal(err)
	}
	for _, sub := range []string{"link", "alias"} {
		_, err := e.create(args{Root: e.rootB, Subdirectory: sub, IdempotencyKey: "key-link-" + sub})
		if codeOf(err) != "FORBIDDEN" {
			t.Errorf("symlinked subdirectory %q: %v", sub, err)
		}
	}
	if n := len(e.requests()); n != 0 {
		t.Fatalf("%d native requests for symlinked spellings", n)
	}
}

func TestCreateMaxDepthAbsentIsZeroAndAGivenDepthIsTheLimit(t *testing.T) {
	e := newCreateEnv(t, nil)
	e.mkdirs(e.rootA, "sub")
	// rootA has no max_depth: only the root itself.
	if _, err := e.create(args{Root: e.rootA, Subdirectory: "sub", IdempotencyKey: "key-depth-0001"}); codeOf(err) != "FORBIDDEN" || !strings.Contains(err.Error(), "max_depth") {
		t.Fatalf("a subdirectory under max_depth 0: %v", err)
	}
	if len(e.requests()) != 0 {
		t.Fatal("a native call for a refused depth")
	}
	e.mustCreate(args{Root: e.rootA, IdempotencyKey: "key-depth-0002"})
	// rootB has max_depth 2: one and two levels are fine, three are not.
	e.mkdirs(e.rootB, "a", "b", "c")
	for _, tc := range []struct {
		sub  string
		want string
	}{{"a", ""}, {"a/b", ""}, {"a/b/c", "FORBIDDEN"}} {
		_, err := e.create(args{Root: e.rootB, Subdirectory: tc.sub, IdempotencyKey: "key-depth-" + strings.ReplaceAll(tc.sub, "/", "-")})
		if tc.want == "" && err != nil && codeOf(err) != "FORBIDDEN" {
			t.Errorf("%s: %v", tc.sub, err)
		}
		if tc.want != "" && codeOf(err) != tc.want {
			t.Errorf("%s: %v, want %s", tc.sub, err, tc.want)
		}
	}
}

// §4.2 first row and the two Git rows: every refusal is local, no native call.
func TestCreatePathRowsOf42RefuseWithoutANativeCall(t *testing.T) {
	e := newCreateEnv(t, func(c *lifecycle.Config) { c.Roots[1].MaxDepth = 3 })
	e.mkdirs(e.rootB, "plain")          // no .git anywhere between it and the root... but the root has one
	e.mkdirs(e.rootB, "nested", "deep") // a directory inside the root's repo: its top level is the root
	gitAt(t, e.rootB+"/nested")         // nested repository: the inner top level is inside the root: fine
	outsideRepo := t.TempDir()          // a gitdir: pointer leaving the root
	gitAt(t, outsideRepo)
	e.mkdirs(e.rootB, "ptr")
	if err := os.WriteFile(filepath.Join(e.rootB, "ptr", ".git"), []byte("gitdir: "+outsideRepo+"/.git\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A root that is not itself a repository, with a project below it that is not one either.
	bare := e.mkdirs(e.rootB, "notgit")
	if err := os.RemoveAll(filepath.Join(e.rootB, ".git")); err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		a    args
		code string
		sub  string
	}{
		"root not configured":              {args{Root: "/nonexistent-root"}, "FORBIDDEN", "not a configured"},
		"subdirectory escapes with ..":     {args{Root: e.rootB, Subdirectory: "../x"}, "INVALID_INPUT", ""},
		"subdirectory is absolute":         {args{Root: e.rootB, Subdirectory: "/etc"}, "INVALID_INPUT", ""},
		"subdirectory contains a control":  {args{Root: e.rootB, Subdirectory: "a\x01b"}, "INVALID_INPUT", ""},
		"subdirectory with a backslash":    {args{Root: e.rootB, Subdirectory: `a\b`}, "INVALID_INPUT", ""},
		"path missing":                     {args{Root: e.rootB, Subdirectory: "missing"}, "NOT_FOUND", "never creates"},
		"no top level inside the root":     {args{Root: e.rootB, Subdirectory: "plain"}, "FORBIDDEN", "non_git"},
		"a plain directory under the root": {args{Root: e.rootB, Subdirectory: "notgit"}, "FORBIDDEN", "non_git"},
		"gitdir pointing outside the root": {args{Root: e.rootB, Subdirectory: "ptr"}, "FORBIDDEN", "not_in_root"},
	} {
		tc.a.IdempotencyKey = "key-rows-" + strings.ReplaceAll(name, " ", "-")
		_, err := e.create(tc.a)
		if codeOf(err) != tc.code || !strings.Contains(fmt.Sprint(err), tc.sub) {
			t.Errorf("%s: %v, want %s containing %q", name, err, tc.code, tc.sub)
		}
	}
	_ = bare
	if n := len(e.requests()); n != 0 {
		t.Fatalf("%d native requests for refused paths: %v", n, e.requests())
	}
	// The Bridge never creates a directory.
	if _, err := os.Stat(filepath.Join(e.rootB, "missing")); !os.IsNotExist(err) {
		t.Fatal("create_session created a directory")
	}
}

func TestCreateIdempotentReplayAndConflict(t *testing.T) {
	e := newCreateEnv(t, nil)
	first := e.mustCreate(args{Root: e.rootA, Title: "same", IdempotencyKey: "key-idem-00001"})
	again := e.mustCreate(args{Root: e.rootA, Title: "same", IdempotencyKey: "key-idem-00001"})
	if first != again || e.count("POST") != 1 {
		t.Fatalf("replay: %+v vs %+v, %d POSTs", first, again, e.count("POST"))
	}
	for name, a := range map[string]args{
		"another title": {Root: e.rootA, Title: "other"},
		"no title":      {Root: e.rootA},
		"another root":  {Root: e.rootB},
	} {
		a.IdempotencyKey = "key-idem-00001"
		if _, err := e.create(a); codeOf(err) != "IDEMPOTENCY_CONFLICT" {
			t.Errorf("%s: %v", name, err)
		}
	}
	if e.count("POST") != 1 {
		t.Fatalf("a conflict wrote: %d POSTs", e.count("POST"))
	}
	// A different key is a new request.
	second := e.mustCreate(args{Root: e.rootA, Title: "same", IdempotencyKey: "key-idem-00002"})
	if second.SessionID == first.SessionID || e.count("POST") != 2 {
		t.Fatalf("a new key: %+v, %d POSTs", second, e.count("POST"))
	}
	// A recorded result is returned even when the cap would now refuse a new request: the
	// replay is looked up before any cap.
	e3 := newCreateEnv(t, func(c *lifecycle.Config) { c.CreatePerHour = 1 })
	a := e3.mustCreate(args{Root: e3.rootA, IdempotencyKey: "key-idem-00003"})
	if b := e3.mustCreate(args{Root: e3.rootA, IdempotencyKey: "key-idem-00003"}); a != b {
		t.Fatalf("replay at the cap: %+v vs %+v", a, b)
	}
	if _, err := e3.create(args{Root: e3.rootA, IdempotencyKey: "key-idem-00004"}); codeOf(err) != "SESSION_LIMIT" {
		t.Fatalf("a new request at the cap: %v", err)
	}
}

func TestCreateConcurrentIdenticalRequestsSendOnePost(t *testing.T) {
	e := newCreateEnv(t, nil)
	release := make(chan struct{})
	e.setPostHook(func(w http.ResponseWriter, r *http.Request, dir string) bool {
		<-release
		return false
	})
	var wg sync.WaitGroup
	outs := make([]CreateOutput, 4)
	errs := make([]error, 4)
	for i := range outs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			outs[i], errs[i] = e.create(args{Root: e.rootA, Title: "same", IdempotencyKey: "key-conc-00001"})
		}(i)
	}
	time.Sleep(100 * time.Millisecond)
	close(release)
	wg.Wait()
	for i := range outs {
		if errs[i] != nil || outs[i] != outs[0] {
			t.Fatalf("request %d: %+v %v (first %+v)", i, outs[i], errs[i], outs[0])
		}
	}
	if e.count("POST") != 1 {
		t.Fatalf("%d POSTs for one key", e.count("POST"))
	}
}

// §4.2: a timeout or a lost response is UNKNOWN_OUTCOME, retryable false, NEVER retried and
// never replayed.
func TestCreateTimeoutIsUnknownOutcomeAndNeverRetriedOrReplayed(t *testing.T) {
	for name, hook := range map[string]func(http.ResponseWriter, *http.Request, string) bool{
		"a timeout": func(w http.ResponseWriter, r *http.Request, _ string) bool {
			select {
			case <-r.Context().Done():
			case <-time.After(3 * time.Second):
			}
			return true
		},
		"a lost response": func(w http.ResponseWriter, r *http.Request, _ string) bool {
			c, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				_ = c.Close()
			}
			return true
		},
		"an unreadable answer": func(w http.ResponseWriter, r *http.Request, _ string) bool {
			_, _ = w.Write([]byte("<<not json"))
			return true
		},
		"an answer with no valid session ID": func(w http.ResponseWriter, r *http.Request, dir string) bool {
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "../x", "directory": dir})
			return true
		},
	} {
		t.Run(name, func(t *testing.T) {
			e := newCreateEnv(t, nil)
			createBound.Store(int64(250 * time.Millisecond))
			e.setPostHook(hook)
			_, err := e.create(args{Root: e.rootA, IdempotencyKey: "key-unk-000001"})
			ee := envelope(err)
			if ee.Code != "UNKNOWN_OUTCOME" || ee.Retryable || !strings.Contains(ee.Message, "not retried") && !strings.Contains(ee.Message, "may exist") {
				t.Fatalf("%v", err)
			}
			if e.count("POST") != 1 {
				t.Fatalf("%d POSTs after one create", e.count("POST"))
			}
			// The same key and input again: the recorded outcome, no second POST.
			_, err2 := e.create(args{Root: e.rootA, IdempotencyKey: "key-unk-000001"})
			if codeOf(err2) != "UNKNOWN_OUTCOME" || e.count("POST") != 1 {
				t.Fatalf("replay of an unknown outcome: %v, %d POSTs", err2, e.count("POST"))
			}
			// The same key with another input is a conflict, still no POST.
			if _, err3 := e.create(args{Root: e.rootA, Title: "x", IdempotencyKey: "key-unk-000001"}); codeOf(err3) != "IDEMPOTENCY_CONFLICT" || e.count("POST") != 1 {
				t.Fatalf("%v, %d POSTs", err3, e.count("POST"))
			}
			// The message names the hint a caller finds the stray session with, never the raw key.
			if strings.Contains(ee.Message, "key-unk-000001") {
				t.Errorf("the raw key is in the error: %s", ee.Message)
			}
		})
	}
}

func TestCreateNative4xxAnd5xxAreUpstreamFailureAndMayBeRetriedByTheCaller(t *testing.T) {
	for _, status := range []int{400, 403, 404, 409, 500, 503} {
		e := newCreateEnv(t, nil)
		e.setPostStatus(status)
		_, err := e.create(args{Root: e.rootA, IdempotencyKey: "key-http-00001"})
		if codeOf(err) != "UPSTREAM_FAILURE" || envelope(err).Retryable {
			t.Fatalf("%d: %v", status, err)
		}
		if e.count("POST") != 1 {
			t.Fatalf("%d: %d POSTs", status, e.count("POST"))
		}
		// A definite refusal is not recorded as uncertain: the caller's own retry is a new POST.
		e.setPostStatus(0)
		if _, err := e.create(args{Root: e.rootA, IdempotencyKey: "key-http-00001"}); err != nil || e.count("POST") != 2 {
			t.Fatalf("%d: retry %v, %d POSTs", status, err, e.count("POST"))
		}
	}
}

func TestCreateDirectoryMismatchIsUnknownOutcomeWithTheBootstrapFlag(t *testing.T) {
	e := newCreateEnv(t, nil)
	other := t.TempDir()
	e.setPostHook(func(w http.ResponseWriter, r *http.Request, dir string) bool {
		_ = json.NewEncoder(w).Encode(opencode.Session{ID: "ses_stray1", Directory: other})
		return true
	})
	out, err := e.create(args{Root: e.rootA, IdempotencyKey: "key-mism-00001"})
	var ce *createError
	if !errors.As(err, &ce) || !ce.BootstrapMayBeOutsideRoot || codeOf(err) != "UNKNOWN_OUTCOME" || envelope(err).Retryable {
		t.Fatalf("%+v %v", out, err)
	}
	if out.SessionID != "" || !strings.Contains(err.Error(), "bootstrap_may_be_outside_root=true") || !strings.Contains(err.Error(), "not used") {
		t.Fatalf("the session was used or the flag is missing: %+v %v", out, err)
	}
	// Replay: the recorded outcome, the flag preserved, no second POST.
	_, err2 := e.create(args{Root: e.rootA, IdempotencyKey: "key-mism-00001"})
	var ce2 *createError
	if !errors.As(err2, &ce2) || !ce2.BootstrapMayBeOutsideRoot || e.count("POST") != 1 {
		t.Fatalf("replay: %v, %d POSTs", err2, e.count("POST"))
	}
	// A directory that only differs by spelling is a mismatch too (the Bridge sent a canonical one).
	e2 := newCreateEnv(t, nil)
	e2.setPostHook(func(w http.ResponseWriter, r *http.Request, dir string) bool {
		_ = json.NewEncoder(w).Encode(opencode.Session{ID: "ses_stray2", Directory: dir + "/"})
		return true
	})
	if _, err := e2.create(args{Root: e2.rootA, IdempotencyKey: "key-mism-00002"}); codeOf(err) != "UNKNOWN_OUTCOME" {
		t.Fatalf("a different spelling: %v", err)
	}
}

func TestCreateIdentityChangedAroundThePostIsUnknownOrRefused(t *testing.T) {
	// Replaced after the check and before the POST: refused, no POST.
	e := newCreateEnv(t, func(c *lifecycle.Config) { c.Roots[1].MaxDepth = 1 })
	e.mkdirs(e.rootB, "p")
	gitAt(t, e.rootB+"/p")
	var swapped atomic.Bool
	e.setListHook(func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Query().Get("directory") == e.rootA && swapped.CompareAndSwap(false, true) {
			p := filepath.Join(e.rootB, "p")
			_ = os.Rename(p, p+"-old")
			e.mkdirs(p)
			gitAt(t, p)
		}
		return false
	})
	_, err := e.create(args{Root: e.rootB, Subdirectory: "p", IdempotencyKey: "key-ident-0001"})
	if codeOf(err) != "FORBIDDEN" || e.count("POST") != 0 {
		t.Fatalf("a directory replaced before the POST: %v, %d POSTs", err, e.count("POST"))
	}
	// Replaced during the POST: UNKNOWN_OUTCOME with the flag.
	e2 := newCreateEnv(t, func(c *lifecycle.Config) { c.Roots[1].MaxDepth = 1 })
	e2.mkdirs(e2.rootB, "q")
	gitAt(t, e2.rootB+"/q")
	e2.setPostHook(func(w http.ResponseWriter, r *http.Request, dir string) bool {
		_ = os.Rename(dir, dir+"-old")
		e2.mkdirs(dir)
		gitAt(t, dir)
		_ = json.NewEncoder(w).Encode(opencode.Session{ID: "ses_moved1", Directory: dir})
		return true
	})
	_, err = e2.create(args{Root: e2.rootB, Subdirectory: "q", IdempotencyKey: "key-ident-0002"})
	var ce *createError
	if !errors.As(err, &ce) || !ce.BootstrapMayBeOutsideRoot || codeOf(err) != "UNKNOWN_OUTCOME" {
		t.Fatalf("a directory replaced during the POST: %v", err)
	}
}

func TestCreateCaps(t *testing.T) {
	t.Run("create_per_hour", func(t *testing.T) {
		e := newCreateEnv(t, func(c *lifecycle.Config) { c.CreatePerHour = 2 })
		e.mustCreate(args{Root: e.rootA, IdempotencyKey: "key-hour-00001"})
		e.mustCreate(args{Root: e.rootA, IdempotencyKey: "key-hour-00002"})
		_, err := e.create(args{Root: e.rootA, IdempotencyKey: "key-hour-00003"})
		if codeOf(err) != "SESSION_LIMIT" || !envelope(err).Retryable || e.count("POST") != 2 {
			t.Fatalf("%v, %d POSTs", err, e.count("POST"))
		}
		e.clock = e.clock.Add(61 * time.Minute) // the window passes
		if _, err := e.create(args{Root: e.rootA, IdempotencyKey: "key-hour-00004"}); err != nil || e.count("POST") != 3 {
			t.Fatalf("after the window: %v, %d POSTs", err, e.count("POST"))
		}
	})
	t.Run("max_sessions of the root", func(t *testing.T) {
		e := newCreateEnv(t, nil)
		for i := 0; i < 3; i++ {
			e.listing[e.rootA] = append(e.listing[e.rootA], opencode.Session{ID: fmt.Sprintf("ses_old%d", i), Directory: e.rootA})
		}
		_, err := e.create(args{Root: e.rootA, IdempotencyKey: "key-max-000001"})
		if codeOf(err) != "SESSION_LIMIT" || e.count("POST") != 0 {
			t.Fatalf("%v, %d POSTs", err, e.count("POST"))
		}
		// Sessions of another directory in the same list are not this root's.
		e2 := newCreateEnv(t, nil)
		for i := 0; i < 5; i++ {
			e2.listing[e2.rootA] = append(e2.listing[e2.rootA], opencode.Session{ID: fmt.Sprintf("ses_other%d", i), Directory: "/somewhere/else"})
		}
		e2.listing[e2.rootA] = append(e2.listing[e2.rootA], opencode.Session{ID: "ses_mine1", Directory: e2.rootA})
		if _, err := e2.create(args{Root: e2.rootA, IdempotencyKey: "key-max-000002"}); err != nil {
			t.Fatalf("below the cap: %v", err)
		}
		// The same ID listed twice counts once.
		e3 := newCreateEnv(t, nil)
		e3.listing[e3.rootA] = []opencode.Session{{ID: "ses_dup", Directory: e3.rootA}, {ID: "ses_dup", Directory: e3.rootA}}
		if _, err := e3.create(args{Root: e3.rootA, IdempotencyKey: "key-max-000003"}); err != nil {
			t.Fatalf("a duplicate counted twice: %v", err)
		}
	})
	t.Run("max_total_sessions", func(t *testing.T) {
		e := newCreateEnv(t, func(c *lifecycle.Config) { c.MaxTotalSessions = 3 })
		e.listing[e.rootA] = []opencode.Session{{ID: "ses_a1", Directory: e.rootA}, {ID: "ses_a2", Directory: e.rootA}}
		e.listing[e.rootB] = []opencode.Session{{ID: "ses_b1", Directory: e.rootB}}
		_, err := e.create(args{Root: e.rootA, IdempotencyKey: "key-total-00001"})
		if codeOf(err) != "SESSION_LIMIT" || !strings.Contains(err.Error(), "max_total_sessions") || e.count("POST") != 0 {
			t.Fatalf("%v, %d POSTs", err, e.count("POST"))
		}
	})
	t.Run("the caps hold under concurrency", func(t *testing.T) {
		e := newCreateEnv(t, func(c *lifecycle.Config) { c.Roots[0].MaxSessions = 2; c.CreatePerHour = 100 })
		e.autoAdd = true
		var wg sync.WaitGroup
		var ok, limited int32
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				_, err := e.create(args{Root: e.rootA, IdempotencyKey: fmt.Sprintf("key-race-%05d", i)})
				switch codeOf(err) {
				case "":
					atomic.AddInt32(&ok, 1)
				case "SESSION_LIMIT":
					atomic.AddInt32(&limited, 1)
				default:
					t.Errorf("%v", err)
				}
			}(i)
		}
		wg.Wait()
		if ok != 2 || limited != 6 || e.count("POST") != 2 {
			t.Fatalf("ok=%d limited=%d POSTs=%d: the cap of 2 must hold under concurrent creates", ok, limited, e.count("POST"))
		}
	})
}

// §3.3: an incomplete or timed-out native count is never treated as zero; no native write.
func TestCreateIncompleteCountRefusesWithNoNativeWrite(t *testing.T) {
	cases := map[string]struct {
		hook func(http.ResponseWriter, *http.Request) bool
		code string
	}{
		"a truncated list": {func(w http.ResponseWriter, r *http.Request) bool {
			l := make([]opencode.Session, createCountWindow+1)
			for i := range l {
				l[i] = opencode.Session{ID: fmt.Sprintf("ses_x%d", i), Directory: "/elsewhere"}
			}
			_ = json.NewEncoder(w).Encode(l)
			return true
		}, "SESSION_LIMIT"},
		"a failing page": {func(w http.ResponseWriter, r *http.Request) bool { w.WriteHeader(500); return true }, "SESSION_LIMIT"},
		"an unreadable answer": {func(w http.ResponseWriter, r *http.Request) bool {
			_, _ = w.Write([]byte("nope"))
			return true
		}, "SESSION_LIMIT"},
		"a timeout": {func(w http.ResponseWriter, r *http.Request) bool {
			select {
			case <-r.Context().Done():
			case <-time.After(3 * time.Second):
			}
			return true
		}, "UPSTREAM_TIMEOUT"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			e := newCreateEnv(t, nil)
			createBound.Store(int64(250 * time.Millisecond))
			e.setListHook(tc.hook)
			_, err := e.create(args{Root: e.rootA, IdempotencyKey: "key-count-0001"})
			if codeOf(err) != tc.code {
				t.Fatalf("%v, want %s", err, tc.code)
			}
			if tc.code == "SESSION_LIMIT" && !strings.Contains(err.Error(), "cap state unknown") {
				t.Errorf("the message does not say the cap state is unknown: %v", err)
			}
			if e.count("POST") != 0 {
				t.Fatalf("a native write after an incomplete count: %d POSTs", e.count("POST"))
			}
			// The refusal is not recorded: the same key succeeds when the count can be read.
			e.setListHook(nil)
			createBound.Store(int64(5 * time.Second))
			if _, err := e.create(args{Root: e.rootA, IdempotencyKey: "key-count-0001"}); err != nil || e.count("POST") != 1 {
				t.Fatalf("retry after a refused count: %v, %d POSTs", err, e.count("POST"))
			}
		})
	}
	// Only a read that stays within the window counts: exactly the window is complete.
	e := newCreateEnv(t, func(c *lifecycle.Config) { c.Roots[0].MaxSessions = 5000; c.MaxTotalSessions = 5000 })
	l := make([]opencode.Session, createCountWindow)
	for i := range l {
		l[i] = opencode.Session{ID: fmt.Sprintf("ses_y%d", i), Directory: "/elsewhere"}
	}
	e.listing[e.rootA] = l
	if _, err := e.create(args{Root: e.rootA, IdempotencyKey: "key-count-0002"}); err != nil {
		t.Fatalf("a list of exactly the window: %v", err)
	}
}

func TestCreateReadOnlyIsForbiddenBeforeAnyWork(t *testing.T) {
	e := newCreateEnv(t, nil)
	e.mustCreate(args{Root: e.rootA, IdempotencyKey: "key-ro-000001"})
	before := len(e.requests())
	e.s.ReadOnly = true
	// Even a request whose outcome is recorded is refused: nothing is read and nothing written.
	for _, a := range []args{{Root: e.rootA, IdempotencyKey: "key-ro-000001"}, {Root: e.rootA, IdempotencyKey: "key-ro-000002"}} {
		if _, err := e.create(a); codeOf(err) != "FORBIDDEN" {
			t.Fatalf("%v", err)
		}
	}
	if len(e.requests()) != before {
		t.Fatal("a native call under BRIDGE_READ_ONLY")
	}
}

func TestCreateInputValidation(t *testing.T) {
	e := newCreateEnv(t, nil)
	for name, a := range map[string]args{
		"title too long":     {Root: e.rootA, Title: strings.Repeat("x", 201)},
		"title with newline": {Root: e.rootA, Title: "a\nb"},
		"title with NUL":     {Root: e.rootA, Title: "a\x00b"},
		"invalid UTF-8":      {Root: e.rootA, Title: "a\xffb"},
	} {
		a.IdempotencyKey = "key-input-0001"
		if _, err := e.create(a); codeOf(err) != "INVALID_INPUT" {
			t.Errorf("%s: %v", name, err)
		}
	}
	for _, k := range []string{"short", strings.Repeat("k", 129)} {
		if err := validateArgs("create_session", args{Root: e.rootA, IdempotencyKey: k}); err != nil {
			continue // rejected earlier (also by the schema)
		}
		if _, err := e.s.call(context.Background(), "create_session", args{Root: e.rootA, IdempotencyKey: k}); codeOf(err) != "INVALID_INPUT" {
			t.Errorf("key of %d bytes: %v", len(k), err)
		}
	}
	// Valid: a 200-character title, a multi-byte title.
	e.mustCreate(args{Root: e.rootA, Title: strings.Repeat("é", 200), IdempotencyKey: "key-input-0002"})
	if n := e.count("POST"); n != 1 {
		t.Fatalf("%d POSTs", n)
	}
	// The registry does not keep the raw key.
	for k := range e.cc.reg.recs {
		if strings.Contains(k, "key-input") {
			t.Errorf("the registry is keyed by the raw key: %q", k)
		}
	}
}

func TestCreateIsNotAMutatingToolOfTheExistingAuditAndWritesNoAuditLine(t *testing.T) {
	if mutating("create_session") {
		t.Fatal("create_session must not use the prompt/interrupt AUDIT-01 path (its own line is slice S5)")
	}
}

// Through the real MCP transport: the schema rejects every field the ADR forbids, the tool is
// listed only when configured, and BRIDGE_READ_ONLY returns a typed FORBIDDEN.
func TestCreateOverTheMCPTransport(t *testing.T) {
	e := newCreateEnv(t, nil)
	pol, err := security.New([]string{e.rootA}, []string{"ses_none"})
	if err != nil {
		t.Fatal(err)
	}
	e.s.Policy = pol
	e.s.Token = strings.Repeat("t", 32)
	ts := httptest.NewServer(e.s)
	t.Cleanup(ts.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	client, err := mcpclient.Connect(ctx, ts.URL, e.s.Token)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	tools, err := client.ListTools(ctx, nil)
	if err != nil || len(tools.Tools) != 12 {
		t.Fatalf("tools %d %v", len(tools.Tools), err)
	}
	out := decode[CreateOutput](t, invoke(t, client, "create_session", map[string]any{"root": e.rootA, "title": "via mcp", "idempotency_key": "key-mcp-000001"}, false))
	if out.SessionID != "opencode:ses_new1" || !out.Created || out.Directory != e.rootA {
		t.Fatalf("%+v", out)
	}
	posts := e.count("POST")
	for _, bad := range []map[string]any{
		{"prompt": "hi"}, {"permission": []any{}}, {"agent": "build"}, {"model": "x"}, {"parentID": "ses_x"}, {"workspaceID": "wrk_x"}, {"harness": "claudecode"},
		{"idempotency_key": "short"}, {"title": ""}, {"title": strings.Repeat("x", 201)},
	} {
		args := map[string]any{"root": e.rootA, "idempotency_key": "key-mcp-bad-001"}
		for k, v := range bad {
			args[k] = v
		}
		cctx, ccancel := context.WithTimeout(context.Background(), 3*time.Second)
		r, err := client.CallTool(cctx, &sdk.CallToolParams{Name: "create_session", Arguments: args})
		ccancel()
		if err == nil && (r == nil || !r.IsError) {
			t.Errorf("%v accepted", bad)
		}
	}
	if e.count("POST") != posts {
		t.Errorf("a rejected request reached the native server")
	}
	// BRIDGE_READ_ONLY: the tool stays listed and refuses with a typed FORBIDDEN.
	e.s.ReadOnly = true
	f := decode[execution.Error](t, invoke(t, client, "create_session", map[string]any{"root": e.rootA, "idempotency_key": "key-mcp-000002"}, true))
	if f.Code != "FORBIDDEN" {
		t.Fatalf("%+v", f)
	}
}

func TestCreateToolIsAbsentWithoutConfiguration(t *testing.T) {
	e := newCreateEnv(t, nil)
	e.s.Create = nil
	if _, err := e.s.call(context.Background(), "create_session", args{Root: e.rootA, IdempotencyKey: "key-none-00001"}); err == nil {
		t.Fatal("create_session served without a configuration")
	}
	e2 := newCreateEnv(t, nil)
	e2.s.Create = nil
	e2.s.Token = strings.Repeat("t", 32)
	ts := httptest.NewServer(e2.s)
	t.Cleanup(ts.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client, err := mcpclient.Connect(ctx, ts.URL, e2.s.Token)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	tools, err := client.ListTools(ctx, nil)
	if err != nil || len(tools.Tools) != 11 {
		t.Fatalf("the default tool set changed: %d %v", len(tools.Tools), err)
	}
}
