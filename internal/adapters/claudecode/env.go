package claudecode

import (
	"errors"
	"path"
	"regexp"
	"strings"
)

// EnvConfig carries the only values the child environment is built from. HOME
// and LANG are those of the Claude service user, supplied by configuration; the
// Bridge's own environment is never consulted (ADR-021 §8.2).
type EnvConfig struct {
	Home string // absolute, clean
	Lang string // e.g. "C.UTF-8"
	Path string // optional; default is a fixed minimal path
}

const defaultChildPath = "/usr/local/bin:/usr/bin:/bin"

var langPattern = regexp.MustCompile(`^[A-Za-z0-9_.@-]{1,64}$`)

// ScrubEnv builds the child's environment from an explicit allowlist. The parent
// environment is accepted only so that the contract is visible in the signature
// and testable with a poisoned value: no byte of it is copied.
func ScrubEnv(parent []string, c EnvConfig) ([]string, error) {
	_ = parent
	if err := cleanAbs(c.Home); err != nil || strings.ContainsAny(c.Home, "=\n\r") {
		return nil, errors.New("claudecode: invalid child HOME")
	}
	if !langPattern.MatchString(c.Lang) {
		return nil, errors.New("claudecode: invalid child LANG")
	}
	p := c.Path
	if p == "" {
		p = defaultChildPath
	}
	for _, el := range strings.Split(p, ":") {
		if err := cleanAbs(el); err != nil || strings.ContainsAny(el, "=\n\r") {
			return nil, errors.New("claudecode: invalid child PATH")
		}
	}
	return []string{
		"PATH=" + p,
		"HOME=" + c.Home,
		"LANG=" + c.Lang,
		"TERM=dumb",
		"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1",
		"DISABLE_AUTOUPDATER=1",
		"DISABLE_UPDATES=1",
	}, nil
}

func cleanAbs(p string) error {
	if p == "" || !path.IsAbs(p) || path.Clean(p) != p || strings.ContainsRune(p, 0) {
		return errors.New("not an absolute clean path")
	}
	return nil
}
