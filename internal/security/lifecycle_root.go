package security

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Errors of the lifecycle-root opener. Callers test them with errors.Is.
var (
	// ErrNotInLifecycleRoot means the path lies in no configured lifecycle root.
	ErrNotInLifecycleRoot = errors.New("path is outside every lifecycle root")
	// ErrLifecycleRootReplaced means a lifecycle root is no longer the directory
	// recorded at startup.
	ErrLifecycleRootReplaced = errors.New("lifecycle root replaced")
	// ErrLifecycleSymlink means a path component is a symlink.
	ErrLifecycleSymlink = errors.New("lifecycle path component is a symlink")
)

// LifecycleRoots is the session-less, confined opener of ADR-022 section 3.2.
//
// It is a new security primitive and deliberately NOT an extension of Policy:
// it knows only the lifecycle roots it was built with, it never consults
// BRIDGE_ALLOWED_ROOTS or BRIDGE_ALLOWED_SESSIONS, and it needs no session. It
// is for read-only metadata reads that must stay inside one lifecycle root. It
// grants nothing to the existing tools.
type LifecycleRoots struct {
	paths []string
	info  []os.FileInfo
}

// NewLifecycleRoots records the identity of every root at startup. Each path
// must be absolute, clean, canonical (no symlink in the path), and a
// directory.
func NewLifecycleRoots(paths []string) (*LifecycleRoots, error) {
	l := &LifecycleRoots{}
	for _, p := range paths {
		if !filepath.IsAbs(p) || filepath.Clean(p) != p || p == string(filepath.Separator) {
			return nil, fmt.Errorf("lifecycle root %q must be an absolute, clean path other than /", p)
		}
		real, err := filepath.EvalSymlinks(p)
		if err != nil || real != p {
			return nil, fmt.Errorf("lifecycle root %q must exist and be canonical", p)
		}
		info, err := os.Stat(p)
		if err != nil || !info.IsDir() {
			return nil, fmt.Errorf("lifecycle root %q must be a directory", p)
		}
		l.paths = append(l.paths, p)
		l.info = append(l.info, info)
	}
	if len(l.paths) == 0 {
		return nil, errors.New("no lifecycle root")
	}
	return l, nil
}

// Paths returns the configured roots.
func (l *LifecycleRoots) Paths() []string { return append([]string(nil), l.paths...) }

// RootOf returns the lifecycle root that lexically contains path (path itself
// included), or "" if none does.
func (l *LifecycleRoots) RootOf(path string) string {
	if l == nil || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return ""
	}
	for _, base := range l.paths {
		if contains(base, path) {
			return base
		}
	}
	return ""
}

func contains(base, path string) bool {
	rel, err := filepath.Rel(base, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// Open returns a handle on the lifecycle root containing path, the path relative
// to it, and the root's path. It refuses a path outside every lifecycle root
// (even one under BRIDGE_ALLOWED_ROOTS), a root that is no longer the directory
// recorded at startup, and any symlink component; os.Root additionally confines
// every later open if a component changes after this check. The caller closes
// the handle.
func (l *LifecycleRoots) Open(path string) (*os.Root, string, string, error) {
	base := l.RootOf(path)
	if base == "" {
		return nil, "", "", ErrNotInLifecycleRoot
	}
	rel, err := filepath.Rel(base, path)
	if err != nil {
		return nil, "", "", ErrNotInLifecycleRoot
	}
	i := 0
	for ; i < len(l.paths) && l.paths[i] != base; i++ {
	}
	fsHook(base)
	root, err := os.OpenRoot(base)
	if err != nil {
		return nil, "", "", fmt.Errorf("lifecycle root unavailable: %w", err)
	}
	st, err := root.Stat(".")
	if err != nil || !os.SameFile(st, l.info[i]) {
		root.Close()
		return nil, "", "", ErrLifecycleRootReplaced
	}
	part := "."
	if rel != "." {
		for _, component := range strings.Split(rel, string(filepath.Separator)) {
			part = filepath.Join(part, component)
			fsHook(filepath.Join(base, part))
			info, err := root.Lstat(part)
			if err != nil {
				root.Close()
				return nil, "", "", err
			}
			if info.Mode()&os.ModeSymlink != 0 {
				root.Close()
				return nil, "", "", ErrLifecycleSymlink
			}
		}
	}
	return root, rel, base, nil
}
