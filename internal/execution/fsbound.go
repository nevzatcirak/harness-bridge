package execution

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/nevzatcirak/harness-bridge/internal/bounded"
)

// Bounded filesystem checks (docs/design/EXEC_FS_BOUNDING.md). A directory on a
// hung NFS/FUSE mount can block EvalSymlinks or Stat in the kernel, and no Go
// context can interrupt that. Every workspace identity check in this package
// therefore runs through a dedicated bounded.Group: a caller stops waiting at
// its own deadline or after fsTimeout, concurrent checks of the same operation
// and path share one call, at most fsLimit calls run at once, and no result is
// cached. An abandoned call is not tracked by Manager.wg, so Close cannot hang
// on it; fsLimit bounds how many such goroutines a hung mount can pin.
const (
	fsTimeout = 2 * time.Second
	fsLimit   = 8
)

// errFSTimeout means a workspace identity check did not complete (caller
// deadline, fsTimeout or fsLimit). The outcome is unknown, never a pass or a
// mismatch. Each call site maps it to its own fail-closed result.
var errFSTimeout = errors.New("workspace filesystem check did not complete")

// fsKey identifies one shareable call. A non-zero fresh nonce makes the key
// unique, so the caller always starts its own call and never joins one that
// started earlier (used by checks that must observe the filesystem after a
// given point, such as the re-validation after native reads).
type fsKey struct {
	op, path string
	fresh    uint64
}

var fsFresh atomic.Uint64

type fsResult struct {
	path string
	info os.FileInfo
	err  error
}

var fsChecks = bounded.NewGroup[fsKey, fsResult](fsLimit, fsTimeout)

// testFSHook, when set by tests, runs before each bounded filesystem call so a
// test can simulate a call that blocks. Nil in production.
var testFSHook atomic.Pointer[func(op, path string)]

// SetFSHookForTesting installs fn as the filesystem hook and returns a function
// that removes it. For tests only.
func SetFSHookForTesting(fn func(op, path string)) (restore func()) {
	testFSHook.Store(&fn)
	return func() { testFSHook.Store(nil) }
}

// fsInFlight reports how many bounded filesystem calls are still running.
func fsInFlight() int { return fsChecks.InFlight() }

func fsHook(op, path string) {
	if hook := testFSHook.Load(); hook != nil {
		(*hook)(op, path)
	}
}

// evalSymlinks is filepath.EvalSymlinks bounded by ctx and fsTimeout. It returns
// errFSTimeout when the call does not complete.
func evalSymlinks(ctx context.Context, path string) (string, error) {
	return evalSymlinksKey(ctx, fsKey{op: "evalsymlinks", path: path})
}

// evalSymlinksFresh is evalSymlinks that never joins an in-flight call: the
// result always comes from a call that started after this one was made.
func evalSymlinksFresh(ctx context.Context, path string) (string, error) {
	return evalSymlinksKey(ctx, fsKey{op: "evalsymlinks", path: path, fresh: fsFresh.Add(1)})
}

func evalSymlinksKey(ctx context.Context, key fsKey) (string, error) {
	path := key.path
	r, e := fsChecks.Do(ctx, key, func(context.Context) fsResult {
		fsHook("evalsymlinks", path)
		resolved, err := filepath.EvalSymlinks(path)
		return fsResult{path: resolved, err: err}
	})
	if e != nil {
		return "", errFSTimeout
	}
	return r.path, r.err
}

// stat is os.Stat bounded by ctx and fsTimeout. It returns errFSTimeout when the
// call does not complete.
func stat(ctx context.Context, path string) (os.FileInfo, error) {
	return statKey(ctx, fsKey{op: "stat", path: path})
}

// statFresh is stat that never joins an in-flight call.
func statFresh(ctx context.Context, path string) (os.FileInfo, error) {
	return statKey(ctx, fsKey{op: "stat", path: path, fresh: fsFresh.Add(1)})
}

func statKey(ctx context.Context, key fsKey) (os.FileInfo, error) {
	path := key.path
	r, e := fsChecks.Do(ctx, key, func(context.Context) fsResult {
		fsHook("stat", path)
		info, err := os.Stat(path)
		return fsResult{info: info, err: err}
	})
	if e != nil {
		return nil, errFSTimeout
	}
	return r.info, r.err
}

// fsUnavailable is the typed MCP error for a workspace check that did not
// complete on a path where nothing was or will be submitted to the native
// harness, so a retry is safe (CONTRACTS.md, UPSTREAM_TIMEOUT retryable).
func fsUnavailable(message string) error {
	return &Error{Code: "UPSTREAM_TIMEOUT", Message: message, Retryable: true}
}
