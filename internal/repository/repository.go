// Package repository derives host-local identities; metadata never grants access.
package repository

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type Repository struct {
	ID              string `json:"repository_id"`
	CommonDirectory string `json:"common_directory"`
}
type Workspace struct {
	ID           string `json:"workspace_id"`
	RepositoryID string `json:"repository_id"`
	Root         string `json:"root"`
	GitDirectory string `json:"git_directory"`
	Branch       string `json:"branch,omitempty"`
	Head         string `json:"head,omitempty"`
}

func Identity(kind string, values ...string) string {
	h := sha256.New()
	for _, v := range values {
		h.Write([]byte(v))
		h.Write([]byte{0})
	}
	return kind + ":" + hex.EncodeToString(h.Sum(nil))
}

type bounded struct{ bytes.Buffer }

func (b *bounded) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 8192 {
		return 0, errors.New("git metadata too large")
	}
	return b.Buffer.Write(p)
}

// Inspect only accepts an already authorized, canonical absolute directory.
// No shell, status scan, hooks, remote transport or repository-defined command runs.
func Inspect(ctx context.Context, directory string) (Repository, Workspace, error) {
	var r Repository
	var w Workspace
	real, e := filepath.EvalSymlinks(directory)
	if e != nil || !filepath.IsAbs(directory) || filepath.Clean(directory) != directory || real != directory {
		return r, w, errors.New("noncanonical workspace")
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	git := func(args ...string) (string, error) {
		cmd := exec.CommandContext(ctx, "/usr/bin/git", append([]string{"--no-optional-locks", "-C", real}, args...)...)
		cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=/nonexistent", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0"}
		var out bounded
		cmd.Stdout = &out
		cmd.Stderr = &bounded{}
		if e := cmd.Run(); e != nil {
			return "", errors.New("git metadata unavailable")
		}
		return strings.TrimSpace(out.String()), nil
	}
	root, e := git("rev-parse", "--show-toplevel")
	if e != nil {
		return r, w, e
	}
	root, e = filepath.EvalSymlinks(root)
	if e != nil {
		return r, w, e
	}
	common, e := git("rev-parse", "--path-format=absolute", "--git-common-dir")
	if e != nil {
		return r, w, e
	}
	common, e = filepath.EvalSymlinks(common)
	if e != nil {
		return r, w, e
	}
	gd, e := git("rev-parse", "--absolute-git-dir")
	if e != nil {
		return r, w, e
	}
	gd, e = filepath.EvalSymlinks(gd)
	if e != nil {
		return r, w, e
	}
	// Paths identify local storage, not a remote URL or native project label.
	host, _ := os.Hostname()
	r = Repository{Identity("repo", host, common), common}
	w = Workspace{ID: Identity("workspace", r.ID, root, gd), RepositoryID: r.ID, Root: root, GitDirectory: gd}
	w.Branch, _ = git("symbolic-ref", "--quiet", "--short", "HEAD")
	w.Head, _ = git("rev-parse", "--verify", "HEAD")
	return r, w, nil
}
