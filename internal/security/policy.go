// Package security implements the single local principal's explicit session ACL.
package security

import (
	"errors"
	"path/filepath"
	"strings"
)

type Policy struct {
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
	if e != nil {
		return false
	}
	for _, root := range p.Roots {
		rel, e := filepath.Rel(root, real)
		if e == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return true
		}
	}
	return false
}
