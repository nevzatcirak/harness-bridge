// Package security implements the single local principal's explicit session ACL.
package security

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
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
func (p *Policy) Allows(id, directory string) bool {
	if p == nil || !p.Sessions[id] || !filepath.IsAbs(directory) {
		return false
	}
	real, e := filepath.EvalSymlinks(directory)
	if e != nil || filepath.Clean(directory) != directory || real != directory {
		return false
	}
	for i, root := range p.Roots {
		rel, e := filepath.Rel(root, real)
		if e == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			info, e := os.Stat(root)
			return e == nil && i < len(p.rootInfo) && os.SameFile(info, p.rootInfo[i])
		}
	}
	return false
}

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
