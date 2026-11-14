package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/nevzatcirak/harness-bridge/internal/bounded"
	"github.com/nevzatcirak/harness-bridge/internal/security"
)

// Codes of a refusal. They are the ADR-022 section 4.2 result codes of the
// authorization step; a caller maps them onto the tool error.
const (
	CodeForbidden    = "FORBIDDEN"
	CodeInvalidInput = "INVALID_INPUT"
	CodeNotFound     = "NOT_FOUND"
)

// Closed reasons carried on a FORBIDDEN refusal (ADR-022 section 3.2, 4.2). A
// refusal with no reason has an empty Reason.
const (
	ReasonNonGit    = "non_git"     // no top level inside the root, or an unsupported layout
	ReasonNotInRoot = "not_in_root" // a gitdir: or commondir target leaves the root
)

// Causes of a refusal: finer than Reason, never returned to a client, for the
// audit line and tests.
const (
	causeNoRoot       = "no_root"          // outside every lifecycle root
	causeMode         = "mode_not_granted" // the root lacks the needed mode
	causeNotCanonical = "not_canonical"    // the directory is not its own canonical path
	causeRootReplaced = "root_replaced"
	causeSymlink      = "symlink"
	causeNotDir       = "not_directory"
	causeChanged      = "changed"
	causeUnavailable  = "unavailable"
	causeNoTopLevel   = "no_top_level" // ascent reached the root without a .git
	causeAscentBound  = "ascent_bound" // more than 128 levels
	causeDotGit       = "dot_git"      // .git is a symlink, unreadable or malformed
	causeGitdir       = "gitdir"       // malformed or multi-line gitdir: file
	causePointer      = "pointer"      // gitdir: / commondir target leaves or cannot be opened
	causeLayout       = "layout"       // no HEAD, reftable, not a directory
)

// ascentLimit is the ascent bound of the Git top level procedure (as inspect).
const ascentLimit = 128

// authBound bounds one authorization and every caller's wait for it (a hung
// NFS/FUSE mount must not hold a request).
const authBound = 2 * time.Second

// AuthError is a refusal of the lifecycle authorization.
type AuthError struct {
	Code   string
	Reason string // closed, "" or ReasonNonGit / ReasonNotInRoot
	Cause  string // closed, for audit and tests
}

func (e *AuthError) Error() string {
	s := e.Code
	if e.Reason != "" {
		s += " (" + e.Reason + ")"
	}
	return "lifecycle authorization refused: " + s + " [" + e.Cause + "]"
}

func refusal(code, reason, cause string) *AuthError {
	return &AuthError{Code: code, Reason: reason, Cause: cause}
}

// Grant is the result of a successful authorization. It records what was
// observed so Recheck can verify, immediately before the native call, that the
// directory identity is unchanged (ADR-014; rule 2 of section 3.2). A Grant is
// per call: it is never cached and is never an authority after the call.
type Grant struct {
	Root      string // the lifecycle root that grants
	Mode      string
	Directory string // canonical session directory
	TopLevel  string // the Git top level, inside Root
	GitDir    string
	CommonDir string // equals GitDir for a main worktree

	a        *Authorizer
	observed []observation
}

// Authorizer applies ADR-022 section 3.2 rules 1 to 3 against the validated
// lifecycle configuration and the live filesystem. It makes no native call and
// runs no subprocess. The existing tools' ACL is not consulted and not changed:
// the per-root existing_tools flag is parsed but grants nothing here.
type Authorizer struct {
	cfg   *Config
	roots *security.LifecycleRoots
	group *bounded.Group[authKey, authResult]
	// between is a test seam, called after rule 1 and before rule 3. Nil in
	// production.
	between func()
}

type authKey struct {
	a         *Authorizer
	directory string
	mode      string
}

type authResult struct {
	grant *Grant
	err   error
}

// NewAuthorizer records the identity of every configured root.
func NewAuthorizer(cfg *Config) (*Authorizer, error) {
	if !cfg.Enabled() {
		return nil, errors.New("lifecycle is not configured")
	}
	var paths []string
	for _, r := range cfg.Roots {
		paths = append(paths, r.Path)
	}
	roots, err := security.NewLifecycleRoots(paths)
	if err != nil {
		return nil, err
	}
	return &Authorizer{cfg: cfg, roots: roots, group: bounded.NewGroup[authKey, authResult](16, authBound)}, nil
}

// Authorize decides whether directory is authorized for mode (ModeCreate or
// ModeArchive). On success it returns a Grant; otherwise an *AuthError, or an error
// wrapping security.ErrCheckUnavailable when the filesystem checks did not
// complete in time (the outcome is unknown and is never an authorization).
func (a *Authorizer) Authorize(ctx context.Context, directory, mode string) (*Grant, error) {
	if a == nil || (mode != ModeCreate && mode != ModeArchive) {
		return nil, refusal(CodeInvalidInput, "", causeMode)
	}
	if !filepath.IsAbs(directory) || filepath.Clean(directory) != directory {
		return nil, refusal(CodeInvalidInput, "", causeNotCanonical)
	}
	res, err := a.group.Do(ctx, authKey{a, directory, mode}, func(context.Context) authResult {
		g, e := a.authorize(directory, mode)
		return authResult{g, e}
	})
	if err != nil {
		return nil, fmt.Errorf("%w: %w", security.ErrCheckUnavailable, err)
	}
	return res.grant, res.err
}

func (a *Authorizer) authorize(directory, mode string) (*Grant, error) {
	// Rule 1: inside a configured root that has the needed mode.
	base := a.roots.RootOf(directory)
	if base == "" {
		return nil, refusal(CodeForbidden, "", causeNoRoot)
	}
	var root Root
	for _, r := range a.cfg.Roots {
		if r.Path == base {
			root = r
		}
	}
	if !root.Allows(mode) {
		return nil, refusal(CodeForbidden, "", causeMode)
	}
	real, err := filepath.EvalSymlinks(directory)
	switch {
	case err != nil && os.IsNotExist(err):
		return nil, refusal(CodeNotFound, "", causeNotDir)
	case err != nil:
		return nil, refusal(CodeForbidden, "", causeUnavailable)
	case real != directory:
		return nil, refusal(CodeForbidden, "", causeNotCanonical)
	}
	c := &confined{roots: a.roots, base: base}
	info, err := c.lstat(directory)
	if err != nil {
		return nil, openRefusal(err)
	}
	if !info.IsDir() {
		return nil, refusal(CodeInvalidInput, "", causeNotDir)
	}
	// Rule 2 is recorded here (device and inode of the accepted directory) and
	// verified again by Grant.Recheck right before the native call.
	if a.between != nil {
		a.between()
	}
	// Rule 3: the Git top level, found without ascending past the root, is
	// inside the same root. No git subprocess; reads confined to the root.
	top, err := c.topLevel(directory)
	if err != nil {
		return nil, err
	}
	if !c.unchanged() {
		return nil, refusal(CodeForbidden, "", causeChanged)
	}
	return &Grant{Root: base, Mode: mode, Directory: directory, TopLevel: top.dir, GitDir: top.gitDir, CommonDir: top.commonDir,
		a: a, observed: c.observations()}, nil
}

// openRefusal maps an error of the confined opener to a refusal.
func openRefusal(err error) error {
	switch {
	case errors.Is(err, security.ErrLifecycleRootReplaced):
		return refusal(CodeForbidden, "", causeRootReplaced)
	case errors.Is(err, security.ErrLifecycleSymlink):
		return refusal(CodeForbidden, "", causeSymlink)
	case errors.Is(err, security.ErrNotInLifecycleRoot):
		return refusal(CodeForbidden, "", causeNoRoot)
	case os.IsNotExist(err):
		return refusal(CodeNotFound, "", causeNotDir)
	}
	return refusal(CodeForbidden, "", causeUnavailable)
}

// Recheck verifies, right before the native call, that the lifecycle root and
// every directory and file the authorization looked at are still the same
// objects (device and inode; size and time for a regular file), and that the Git
// top level and the config scan of rule 3 still give the same answer when run
// again. A change refuses with FORBIDDEN; it is never repaired by re-running the
// decision.
func (g *Grant) Recheck(ctx context.Context) error {
	if g == nil || g.a == nil {
		return refusal(CodeForbidden, "", causeChanged)
	}
	res, err := g.a.group.Do(ctx, authKey{g.a, g.Directory, "recheck:" + g.Mode}, func(context.Context) authResult {
		c := &confined{roots: g.a.roots, base: g.Root, seen: g.observed}
		if !c.unchanged() {
			return authResult{err: refusal(CodeForbidden, "", causeChanged)}
		}
		// Rule 3's evidence is refreshed too (#232 P3-4): the bounded top level and
		// config scan run again, from the root, so a config rewritten to the same size
		// and time, or a new .git between the directory and the old top level, is seen.
		// Any difference, and any refusal, is a change.
		fresh := &confined{roots: g.a.roots, base: g.Root}
		top, err := fresh.topLevel(g.Directory)
		if err != nil || top.dir != g.TopLevel || top.gitDir != g.GitDir || top.commonDir != g.CommonDir {
			return authResult{err: refusal(CodeForbidden, "", causeChanged)}
		}
		return authResult{}
	})
	if err != nil {
		return fmt.Errorf("%w: %w", security.ErrCheckUnavailable, err)
	}
	return res.err
}
