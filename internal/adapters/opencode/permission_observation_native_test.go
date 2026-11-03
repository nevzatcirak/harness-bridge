package opencode

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"
)

// Opt-in real transport smoke ONLY. It does not create a native session, prompt,
// pending request or reply. A real pending-permission E2E remains NOT_VERIFIED.
func TestPermissionObservationNativeReadOnlySmoke(t *testing.T) {
	binary := os.Getenv("HB_PERMISSION_READONLY_E2E_BINARY")
	if binary == "" || runtime.GOOS != "linux" {
		t.Skip("opt-in disposable Linux1.19.4 smoke; real pending E2E NOT_VERIFIED")
	}
	if !filepath.IsAbs(binary) {
		t.Fatal("explicit absolute disposable native binary required")
	}
	root := t.TempDir()
	for _, p := range []string{"config", "data", "cache", "state", "home", "managed", "tmp", "workspace"} {
		if err := os.MkdirAll(filepath.Join(root, p), 0700); err != nil {
			t.Fatal(err)
		}
	}
	// No inherited provider/server credentials or production config. Do not alter
	// this test process's environment. TEST_HOME and managed-config overrides are
	// verified in the pinned native source, not guessed HOME behavior.
	env := []string{
		"PATH=" + os.Getenv("PATH"), "LANG=C", "TMPDIR=" + filepath.Join(root, "tmp"),
		"XDG_CONFIG_HOME=" + filepath.Join(root, "config"), "XDG_DATA_HOME=" + filepath.Join(root, "data"),
		"XDG_CACHE_HOME=" + filepath.Join(root, "cache"), "XDG_STATE_HOME=" + filepath.Join(root, "state"),
		"OPENCODE_TEST_HOME=" + filepath.Join(root, "home"), "OPENCODE_TEST_MANAGED_CONFIG_DIR=" + filepath.Join(root, "managed"),
		"OPENCODE_CONFIG_DIR=" + filepath.Join(root, "config"), "OPENCODE_CONFIG_CONTENT={\"plugin\":[],\"permission\":\"ask\"}",
		"OPENCODE_DISABLE_PROJECT_CONFIG=true", "OPENCODE_DISABLE_MODELS_FETCH=true", "OPENCODE_DISABLE_AUTOUPDATE=true",
		"OPENCODE_DISABLE_PRUNE=true", "OPENCODE_EXPERIMENTAL_DISABLE_FILEWATCHER=true",
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	version := exec.CommandContext(ctx, binary, "--version")
	version.Env, version.Dir = env, filepath.Join(root, "workspace")
	output, err := version.Output()
	if err != nil || strings.TrimSpace(string(output)) != "1.19.4" {
		t.Fatal("required isolated OpenCode1.19.4 binary not verified")
	}
	cmd := exec.CommandContext(ctx, binary, "serve", "--hostname", "127.0.0.1", "--port", "0")
	cmd.Env, cmd.Dir, cmd.Stderr = env, filepath.Join(root, "workspace"), io.Discard
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = cmd.Start(); err != nil {
		t.Fatal("disposable native start failed")
	}
	// Only our exact child handle is terminated; Wait always reaps it on failure.
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	address := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(io.LimitReader(stdout, 64<<10))
		pattern := regexp.MustCompile(`http://127\.0\.0\.1:[0-9]+`)
		for scanner.Scan() {
			if u := pattern.FindString(scanner.Text()); u != "" {
				address <- u
				_, _ = io.Copy(io.Discard, stdout)
				return
			}
		}
		address <- ""
	}()
	var base string
	select {
	case base = <-address:
	case <-ctx.Done():
		t.Fatal("disposable native startup deadline")
	}
	if base == "" {
		t.Fatal("disposable native listener unavailable")
	}
	c, err := New(base, "", "")
	if err != nil {
		t.Fatal(err)
	}
	// No real native execution/session is fabricated. This synthetic authorized
	// filter is only for empty-list and server.connected transport smoke; full
	// Snapshot/execution attribution deliberately cannot pass without a session.
	a := &observationAuthorityFixture{scope: PermissionScope{SessionID: "ses_empty_filter", Principal: "smoke", Directory: cmd.Dir, EndpointID: c.InstanceID(), NativeVersion: "1.19.4", PolicyRevision: 1}}
	o, err := NewPermissionObserver(ctx, c, "ses_empty_filter", a, PermissionObservationConfig{})
	if err != nil {
		t.Fatal(err)
	}
	var pending []json.RawMessage
	if err = o.readJSON(ctx, "/permission", permissionSnapshotLimit, &pending); err != nil || pending == nil || len(pending) != 0 {
		t.Fatal("fresh disposable native pending list not empty or unavailable", err)
	}
	response, err := o.get(ctx, "/event", true)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	scanner := bufio.NewScanner(io.LimitReader(response.Body, permissionPayloadLimit))
	connected := false
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		var event nativePermissionEvent
		if json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(line, "data:"))), &event) != nil || event.Type != "server.connected" {
			t.Fatal("unexpected disposable native connected event")
		}
		connected = true
		break
	}
	if !connected {
		t.Fatal("native SSE connected event unavailable")
	}
	t.Logf("real OpenCode1.19.4 child pid=%d parent=%d: empty pending GET + SSE connected PASS; session/prompt/reply calls=0; real pending E2E NOT_VERIFIED", cmd.Process.Pid, os.Getpid())
}
