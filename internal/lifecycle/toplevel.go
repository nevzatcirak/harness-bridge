package lifecycle

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/nevzatcirak/harness-bridge/internal/security"
)

// observation is one object the authorization looked at.
type observation struct {
	path string
	info os.FileInfo
}

// confined reads Git metadata inside ONE lifecycle root through the
// session-less opener, and remembers what it saw. It runs no subprocess.
type confined struct {
	roots *security.LifecycleRoots
	base  string
	seen  []observation
}

func (c *confined) observations() []observation { return append([]observation(nil), c.seen...) }

func (c *confined) observe(path string, info os.FileInfo) {
	c.seen = append(c.seen, observation{path, info})
}

// lstat opens path through the confined opener (a symlink component or a root
// replaced after startup is refused) and Lstats it.
func (c *confined) lstat(path string) (os.FileInfo, error) {
	h, rel, _, err := c.roots.Open(path)
	if err != nil {
		return nil, err
	}
	defer h.Close()
	info, err := h.Lstat(rel)
	if err != nil {
		return nil, err
	}
	c.observe(path, info)
	return info, nil
}

// child Lstats name inside the directory dir, which is opened confined first.
// It returns the raw Lstat error so a genuinely absent entry is distinguishable
// from a refusal. A symlink is returned as such, not followed.
func (c *confined) child(dir, name string) (os.FileInfo, error) {
	h, rel, _, err := c.roots.Open(dir)
	if err != nil {
		return nil, err
	}
	defer h.Close()
	info, err := h.Lstat(filepath.Join(rel, name))
	if err == nil {
		c.observe(filepath.Join(dir, name), info)
	}
	return info, err
}

func (c *confined) read(path string, limit int) ([]byte, error) {
	h, rel, _, err := c.roots.Open(path)
	if err != nil {
		return nil, err
	}
	defer h.Close()
	b, info, err := readRegular(h, rel, limit)
	if err != nil {
		return nil, err
	}
	c.observe(path, info)
	return b, nil
}

// unchanged reports whether every observed object is still the same object:
// the same device and inode, and for a regular file the same size and time. The
// time of a directory is not compared (git writes inside it legitimately).
func (c *confined) unchanged() bool {
	for _, o := range c.seen {
		h, rel, _, err := c.roots.Open(o.path)
		if err != nil {
			return false
		}
		now, err := h.Lstat(rel)
		h.Close()
		if err != nil || !os.SameFile(o.info, now) {
			return false
		}
		if o.info.Mode().IsRegular() && (o.info.Size() != now.Size() || !o.info.ModTime().Equal(now.ModTime())) {
			return false
		}
	}
	return true
}

type topLevel struct {
	dir, gitDir, commonDir string
}

func nonGit(cause string) *AuthError { return refusal(CodeForbidden, ReasonNonGit, cause) }
func notInRoot(cause string) *AuthError {
	return refusal(CodeForbidden, ReasonNotInRoot, cause)
}

// inRoot is the lexical containment check on an already cleaned path.
func inRoot(base, p string) bool {
	rel, err := filepath.Rel(base, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func pointerPath(base, value string) string {
	if filepath.IsAbs(value) {
		return filepath.Clean(value)
	}
	return filepath.Clean(filepath.Join(base, value))
}

// topLevel is the Git top level procedure of ADR-022 section 3.2: ascend one
// level at a time from the canonical session directory, never past the
// lifecycle root, at most 128 levels, and take the nearest .git.
func (c *confined) topLevel(directory string) (topLevel, error) {
	dir := directory
	for level := 0; level < ascentLimit; level++ {
		info, err := c.child(dir, ".git")
		switch {
		case err == nil && info.Mode()&os.ModeSymlink != 0:
			return topLevel{}, nonGit(causeDotGit) // never ascend past a symlinked .git
		case err == nil && info.IsDir():
			return c.finish(dir, filepath.Join(dir, ".git"))
		case err == nil && info.Mode().IsRegular():
			gitDir, e := c.gitdirFile(dir)
			if e != nil {
				return topLevel{}, e
			}
			return c.finish(dir, gitDir)
		case err == nil:
			return topLevel{}, nonGit(causeDotGit)
		case !os.IsNotExist(err):
			return topLevel{}, nonGit(causeDotGit) // unreadable: fail closed, never ascend
		}
		// .git is genuinely absent: go up, but never past the lifecycle root.
		if dir == c.base {
			return topLevel{}, nonGit(causeNoTopLevel)
		}
		parent := filepath.Dir(dir)
		if parent == dir || !inRoot(c.base, parent) {
			return topLevel{}, nonGit(causeNoTopLevel)
		}
		dir = parent
	}
	return topLevel{}, nonGit(causeAscentBound)
}

// gitdirFile reads a ".git" file: a single line "gitdir: <path>". The pointer
// is lexically cleaned, its containment in the lifecycle root is checked on the
// cleaned string, and only then is it opened through the confined opener.
func (c *confined) gitdirFile(dir string) (string, error) {
	raw, err := c.read(filepath.Join(dir, ".git"), 4096)
	if err != nil {
		return "", nonGit(causeGitdir)
	}
	s := string(raw)
	s = strings.TrimSuffix(s, "\n")
	if strings.ContainsAny(s, "\r\n\x00") || !strings.HasPrefix(s, "gitdir: ") || strings.TrimPrefix(s, "gitdir: ") == "" {
		return "", nonGit(causeGitdir)
	}
	return c.target(dir, strings.TrimPrefix(s, "gitdir: "))
}

// target resolves a gitdir: or commondir value: cleaned lexically, contained in
// the root on the cleaned string, then opened confined, and a directory.
func (c *confined) target(base, value string) (string, error) {
	p := pointerPath(base, value)
	if !inRoot(c.base, p) {
		return "", notInRoot(causePointer)
	}
	info, err := c.lstat(p)
	if err != nil {
		return "", notInRoot(causePointer) // cannot be opened through the confined storage
	}
	if !info.IsDir() {
		return "", nonGit(causeLayout)
	}
	return p, nil
}

// finish validates the git directory and its common directory, then applies the
// D16 Q9 (i) scan of the repository config files.
func (c *confined) finish(top, gitDir string) (topLevel, error) {
	if info, err := c.lstat(gitDir); err != nil || !info.IsDir() {
		return topLevel{}, nonGit(causeLayout)
	}
	common := gitDir
	switch info, err := c.child(gitDir, "commondir"); {
	case err == nil:
		if !info.Mode().IsRegular() {
			return topLevel{}, nonGit(causeLayout)
		}
		raw, e := c.read(filepath.Join(gitDir, "commondir"), 4096)
		v := strings.TrimSuffix(string(raw), "\n")
		if e != nil || v == "" || strings.ContainsAny(v, "\r\n\x00") {
			return topLevel{}, nonGit(causeLayout)
		}
		p, e2 := c.target(gitDir, v)
		if e2 != nil {
			return topLevel{}, e2
		}
		common = p
	case !os.IsNotExist(err):
		return topLevel{}, nonGit(causeLayout)
	}
	// Reftable is a layout the bounded files-backend reader does not support.
	if _, err := c.child(common, "reftable"); !os.IsNotExist(err) {
		return topLevel{}, nonGit(causeLayout)
	}
	if info, err := c.child(gitDir, "HEAD"); err != nil || !info.Mode().IsRegular() {
		return topLevel{}, nonGit(causeLayout)
	}
	seen := map[string]bool{}
	for _, d := range []string{common, gitDir} {
		for _, name := range []string{"config", "config.worktree"} {
			p := filepath.Join(d, name)
			if seen[p] {
				continue
			}
			seen[p] = true
			if cause := c.scan(p); cause != "" {
				return topLevel{}, nonGit(cause)
			}
		}
	}
	return topLevel{dir: top, gitDir: gitDir, commonDir: common}, nil
}

// scan reads one config file, if present, and runs scanGitConfig. A file that
// is present but cannot be read completely refuses: it is not known to be safe.
func (c *confined) scan(path string) string {
	info, err := c.child(filepath.Dir(path), filepath.Base(path))
	switch {
	case err != nil && os.IsNotExist(err):
		return "" // an absent config has no settings
	case err != nil || !info.Mode().IsRegular():
		return causeConfigMalform
	}
	data, err := c.read(path, maxGitConfigBytes)
	if err != nil {
		if errors.Is(err, errUnreadable) {
			return causeConfigBounds
		}
		return causeConfigMalform
	}
	return scanGitConfig(data)
}
