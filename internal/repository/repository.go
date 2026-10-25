// Package repository reads a small, confined subset of Git metadata. It never
// invokes Git, parses config, follows refs through Git, or grants session access.
package repository

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/nevzatcirak/harness-bridge/internal/security"
)

type Repository struct {
	ID              string `json:"repository_id"`
	CommonDirectory string `json:"common_directory"`
	DisplayName     string `json:"display_name,omitempty"`
}
type Workspace struct {
	ID             string `json:"workspace_id"`
	RepositoryID   string `json:"repository_id"`
	Root           string `json:"root"`
	GitDirectory   string `json:"git_directory"`
	DisplayName    string `json:"display_name,omitempty"`
	Branch         string `json:"branch,omitempty"`
	Head           string `json:"head,omitempty"`
	LinkedWorktree bool   `json:"linked_worktree,omitempty"`
}

func Identity(kind string, values ...string) string {
	h := sha256.New()
	for _, v := range values {
		h.Write([]byte(v))
		h.Write([]byte{0})
	}
	return kind + ":" + hex.EncodeToString(h.Sum(nil))
}
func display(v string) string {
	v = strings.Map(func(r rune) rune {
		if !unicode.IsPrint(r) {
			return -1
		}
		return r
	}, v)
	r := []rune(v)
	if len(r) > 128 {
		r = r[:128]
	}
	return string(r)
}

var unavailable = errors.New("repository metadata unavailable")

type storage struct {
	policy   *security.Policy
	session  string
	ctx      context.Context
	observed map[string]os.FileInfo
}

func (s *storage) stat(path string) (os.FileInfo, error) {
	if s.ctx.Err() != nil {
		return nil, unavailable
	}
	root, rel, e := s.policy.OpenStorage(s.session, path)
	if e != nil {
		return nil, unavailable
	}
	defer root.Close()
	info, e := root.Lstat(rel)
	if e != nil || info.Mode()&os.ModeSymlink != 0 {
		return nil, unavailable
	}
	s.observed[path] = info
	return info, nil
}
func (s *storage) read(path string, limit int) (string, error) {
	if s.ctx.Err() != nil {
		return "", unavailable
	}
	root, rel, e := s.policy.OpenStorage(s.session, path)
	if e != nil {
		return "", unavailable
	}
	defer root.Close()
	value, info, e := readRegular(root, rel, limit)
	if e != nil {
		return "", unavailable
	}
	s.observed[path] = info
	return strings.TrimSpace(value), nil
}
func (s *storage) unchanged() bool {
	for path, old := range s.observed {
		if s.ctx.Err() != nil {
			return false
		}
		root, rel, e := s.policy.OpenStorage(s.session, path)
		if e != nil {
			return false
		}
		now, e := root.Lstat(rel)
		root.Close()
		if e != nil || !os.SameFile(old, now) || old.Size() != now.Size() || !old.ModTime().Equal(now.ModTime()) {
			return false
		}
	}
	return true
}
func pointer(base, value string) string {
	if filepath.IsAbs(value) {
		return filepath.Clean(value)
	}
	return filepath.Clean(filepath.Join(base, value))
}
func revision(v string) bool {
	if len(v) != 40 && len(v) != 64 {
		return false
	}
	_, e := hex.DecodeString(v)
	return e == nil
}
func validRef(ref string) bool {
	return strings.HasPrefix(ref, "refs/") && !strings.Contains(ref, "..") && !strings.ContainsAny(ref, "\\\x00\r\n :~^?*[") && filepath.Clean(ref) == ref && len(ref) <= 1024
}

// Inspect requires the authoritative session policy before opening any storage.
// Unsupported layouts or unsafe/unavailable storage produce no metadata.
func Inspect(ctx context.Context, directory string, policy *security.Policy, session string) (Repository, Workspace, error) {
	var r Repository
	var w Workspace
	if !policy.Allows(session, directory) {
		return r, w, unavailable
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	s := storage{policy, session, ctx, map[string]os.FileInfo{}}
	root := directory
	gd := ""
	for depth := 0; depth < 128; depth++ {
		if _, e := s.stat(root); e != nil {
			return r, w, unavailable
		}
		candidate := filepath.Join(root, ".git")
		handle, rel, e := policy.OpenStorage(session, candidate)
		if e == nil {
			info, e := handle.Lstat(rel)
			handle.Close()
			if e != nil {
				return r, w, unavailable
			}
			if info.IsDir() {
				gd = candidate
			} else {
				value, e := s.read(candidate, 4096)
				if e != nil || !strings.HasPrefix(value, "gitdir: ") || strings.ContainsAny(value, "\r\n") {
					return r, w, unavailable
				}
				gd = pointer(root, strings.TrimPrefix(value, "gitdir: "))
			}
			break
		}
		// Only a genuinely absent .git permits ascending, never a symlink or denied pointer.
		h, rr, e := policy.OpenStorage(session, root)
		if e != nil {
			return r, w, unavailable
		}
		_, e = h.Lstat(filepath.Join(rr, ".git"))
		h.Close()
		if !os.IsNotExist(e) {
			return r, w, unavailable
		}
		parent := filepath.Dir(root)
		if parent == root {
			return r, w, unavailable
		}
		root = parent
	}
	if gd == "" {
		return r, w, unavailable
	}
	info, e := s.stat(gd)
	if e != nil || !info.IsDir() {
		return r, w, unavailable
	}
	common := gd
	// Test existence through the already confined directory. A denied commondir
	// target is never opened. Missing commondir is the normal main-worktree case.
	h, rel, e := policy.OpenStorage(session, gd)
	if e != nil {
		return r, w, unavailable
	}
	_, ce := h.Lstat(filepath.Join(rel, "commondir"))
	h.Close()
	if ce == nil {
		v, e := s.read(filepath.Join(gd, "commondir"), 4096)
		if e != nil || v == "" || strings.ContainsAny(v, "\r\n") {
			return r, w, unavailable
		}
		common = pointer(gd, v)
	} else if !os.IsNotExist(ce) {
		return r, w, unavailable
	}
	info, e = s.stat(common)
	if e != nil || !info.IsDir() {
		return r, w, unavailable
	}
	// Reftable is intentionally unsupported by this bounded files-backend reader.
	h, rel, e = policy.OpenStorage(session, common)
	if e != nil {
		return r, w, unavailable
	}
	_, reftableErr := h.Lstat(filepath.Join(rel, "reftable"))
	h.Close()
	if !os.IsNotExist(reftableErr) {
		return r, w, unavailable
	}
	head, e := s.read(filepath.Join(gd, "HEAD"), 4096)
	if e != nil {
		return r, w, unavailable
	}
	branch := ""
	rev := ""
	if strings.HasPrefix(head, "ref: ") {
		ref := strings.TrimPrefix(head, "ref: ")
		if !validRef(ref) {
			return r, w, unavailable
		}
		if strings.HasPrefix(ref, "refs/heads/") {
			branch = display(strings.TrimPrefix(ref, "refs/heads/"))
		}
		value, e := s.read(filepath.Join(common, ref), 4096)
		if e == nil {
			if !revision(value) {
				return r, w, unavailable
			}
			rev = value
		} else {
			// An absent loose ref can be unborn or packed. Other failures stay closed.
			// Preserve the distinction between a genuinely missing ref and a
			// rejected symlink/denied path, including a missing symlink target.
			refRoot, _, re := policy.OpenStorage(session, filepath.Join(common, ref))
			if refRoot != nil {
				refRoot.Close()
			}
			if !os.IsNotExist(re) {
				return r, w, unavailable
			}
			h, rr, e := policy.OpenStorage(session, common)
			if e != nil {
				return r, w, unavailable
			}
			_, pe := h.Lstat(filepath.Join(rr, "packed-refs"))
			h.Close()
			if pe == nil {
				packed, e := s.read(filepath.Join(common, "packed-refs"), 1<<20)
				if e != nil {
					return r, w, unavailable
				}
				for _, line := range strings.Split(packed, "\n") {
					fields := strings.Fields(line)
					if len(fields) == 2 && fields[1] == ref {
						if !revision(fields[0]) {
							return r, w, unavailable
						}
						rev = fields[0]
						break
					}
				}
			} else if !os.IsNotExist(pe) {
				return r, w, unavailable
			}
		}
	} else if revision(head) {
		rev = head
	} else {
		return r, w, unavailable
	}
	if !s.unchanged() {
		return r, w, unavailable
	}
	host, _ := os.Hostname()
	r = Repository{ID: Identity("repo", host, common), CommonDirectory: common, DisplayName: display(filepath.Base(root))}
	w = Workspace{ID: Identity("workspace", r.ID, root, gd), RepositoryID: r.ID, Root: root, GitDirectory: gd, DisplayName: display(filepath.Base(root)), Branch: branch, Head: rev, LinkedWorktree: common != gd}
	// A repository name must not depend on which linked workspace was visited first.
	if common != gd {
		r.DisplayName = display(filepath.Base(filepath.Dir(common)))
	}
	return r, w, nil
}
