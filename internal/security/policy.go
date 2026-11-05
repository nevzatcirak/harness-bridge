// Package security implements the single local principal's explicit session ACL.
package security

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/nevzatcirak/harness-bridge/internal/bounded"
)

type Policy struct {
	rootInfo []os.FileInfo
	Roots    []string
	Sessions map[string]bool
}

func New(roots, sessions []string) (*Policy, error) {
	p := &Policy{Sessions: map[string]bool{}}
	for _, root := range roots {
		if !filepath.IsAbs(root) {
			return nil, errors.New("allowed roots must be absolute")
		}
		v, e := filepath.EvalSymlinks(root)
		if e != nil {
			return nil, e
		}
		info, e := os.Stat(v)
		if e != nil {
			return nil, e
		}
		if !info.IsDir() {
			return nil, errors.New("allowed root must be a directory")
		}
		p.rootInfo = append(p.rootInfo, info)
		p.Roots = append(p.Roots, v)
	}
	for _, id := range sessions {
		if id != "" {
			p.Sessions[id] = true
		}
	}
	if len(p.Roots) == 0 || len(p.Sessions) == 0 {
		return nil, errors.New("explicit allowed roots and session IDs required")
	}
	return p, nil
}

// Allows reports whether session id may use directory. It performs blocking
// filesystem calls synchronously; request paths use AllowsContext instead.
func (p *Policy) Allows(id, directory string) bool {
	if p == nil || !p.Sessions[id] || !filepath.IsAbs(directory) {
		return false
	}
	return p.directoryAllowed(directory)
}

// ErrCheckUnavailable means the workspace check could not complete in time
// (for example storage on a hung NFS/FUSE mount). The directory must then be
// treated as not allowed; the outcome is unknown, not a denial.
var ErrCheckUnavailable = errors.New("workspace authorization check unavailable")

// allowKey shares one in-flight filesystem check per policy and directory. The
// session part of the decision is a map lookup done before the check.
type allowKey struct {
	policy    *Policy
	directory string
}

// allowChecks bounds the filesystem half of AllowsContext across the process:
// at most 16 checks still running, each bounded by 2 s for every caller's wait
// (H4-A F-3). Results are not cached beyond one in-flight check.
var allowChecks = bounded.NewGroup[allowKey, bool](16, 2*time.Second)

// AllowsContext is Allows for request paths. A check that does not complete
// before ctx ends, within 2 s, or because 16 checks are already stuck returns
// (false, ErrCheckUnavailable): never allowed by default. Concurrent callers for
// the same policy and directory share one check.
func (p *Policy) AllowsContext(ctx context.Context, id, directory string) (bool, error) {
	if p == nil || !p.Sessions[id] || !filepath.IsAbs(directory) {
		return false, nil
	}
	ok, e := allowChecks.Do(ctx, allowKey{p, directory}, func(context.Context) bool { return p.directoryAllowed(directory) })
	if e != nil {
		return false, fmt.Errorf("%w: %w", ErrCheckUnavailable, e)
	}
	return ok, nil
}

// directoryAllowed is the filesystem half of the decision: the directory is its
// own canonical path and lies under an approved root that is still the same
// directory as at startup.
func (p *Policy) directoryAllowed(directory string) bool {
	fsHook(directory)
	real, e := filepath.EvalSymlinks(directory)
	if e != nil || filepath.Clean(directory) != directory || real != directory {
		return false
	}
	for i, root := range p.Roots {
		rel, e := filepath.Rel(root, real)
		if e == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			fsHook(root)
			info, e := os.Stat(root)
			return e == nil && i < len(p.rootInfo) && os.SameFile(info, p.rootInfo[i])
		}
	}
	return false
}

// testFSHook, when set by tests, runs before each filesystem call of the
// workspace check so a test can simulate a call that blocks. Nil in production.
var testFSHook atomic.Pointer[func(path string)]

func fsHook(path string) {
	if hook := testFSHook.Load(); hook != nil {
		(*hook)(path)
	}
}

// SetFSHookForTesting installs fn as the workspace-check filesystem hook and
// returns a function that removes it. For tests only.
func SetFSHookForTesting(fn func(path string)) (restore func()) {
	testFSHook.Store(&fn)
	return func() { testFSHook.Store(nil) }
}

// AllowsInFlightForTesting reports how many workspace checks are still running.
func AllowsInFlightForTesting() int { return allowChecks.InFlight() }

// OpenStorage returns a confined handle only for an existing session's approved
// workspace storage. It is not a new read grant or a prompt authorization API.
func (p *Policy) OpenStorage(id, path string) (*os.Root, string, error) {
	if p == nil || !p.Sessions[id] || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, "", errors.New("storage not authorized")
	}
	for i, base := range p.Roots {
		rel, e := filepath.Rel(base, path)
		if e != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		root, e := os.OpenRoot(base)
		if e != nil {
			return nil, "", errors.New("storage unavailable")
		}
		st, e := root.Stat(".")
		if e != nil || i >= len(p.rootInfo) || !os.SameFile(st, p.rootInfo[i]) {
			root.Close()
			return nil, "", errors.New("storage root replaced")
		}
		// Reject observed symlinks. os.Root also enforces confinement if a component
		// changes between this check and a subsequent open.
		part := "."
		for _, component := range strings.Split(rel, string(filepath.Separator)) {
			part = filepath.Join(part, component)
			info, e := root.Lstat(part)
			if e != nil {
				root.Close()
				return nil, "", e
			}
			if info.Mode()&os.ModeSymlink != 0 {
				root.Close()
				return nil, "", errors.New("storage symlink rejected")
			}
		}
		return root, rel, nil
	}
	return nil, "", errors.New("storage outside approved roots")
}
