//go:build unix

package acceptance

import (
	"errors"
	"io"
	"os"
	"strings"
)

// maxCanaryEntries bounds the scan: a flood of entries saturates the count instead of being walked.
const maxCanaryEntries = 256

// FileCanaryScan counts the entries of the canary directory (ADR-021 §8.3: after the denied run "no
// canary file exists"). The hostile root's hook, apiKeyHelper and .mcp.json server are configured by
// the operator to write into this one directory; every entry of any kind (file, directory, symlink)
// counts, because anything the hostile root creates there is a hit.
//
// The scan only ever opens that directory: it walks from "/" with O_DIRECTORY|O_NOFOLLOW (the decoy
// filesystem's walk, never creating anything), so a symlink swapped in for the directory or any
// ancestor makes the scan unobserved instead of counting somewhere else. The directory may not be, lie
// inside or contain the run user's denied directories (~/.claude, ~/.config/anthropic): the deny rule
// is never lifted, a real credential directory is never read, and no file content is ever opened.
type FileCanaryScan struct {
	dir string
}

// NewFileCanaryScan validates dir and takes the baseline: the directory must exist and be empty, so
// that a later count of zero is a measurement and not a leftover state.
func NewFileCanaryScan(dir, runHome string) (*FileCanaryScan, error) {
	if !cleanAbs(dir) || !cleanAbs(runHome) {
		return nil, errors.New("acceptance: the canary directory and the run home must be absolute clean paths")
	}
	for _, denied := range []string{runHome + "/.claude", runHome + "/.config/anthropic"} {
		if dir == denied || strings.HasPrefix(dir, denied+"/") || strings.HasPrefix(denied, dir+"/") {
			return nil, errors.New("acceptance: the canary directory may not be, lie inside or contain a denied directory")
		}
	}
	f := &FileCanaryScan{dir: dir}
	n, ok := f.Count()
	if !ok {
		return nil, errors.New("acceptance: the canary directory cannot be read (it must exist, with no symlink in its path)")
	}
	if n != 0 {
		return nil, errors.New("acceptance: the canary directory is not empty (the baseline must be zero)")
	}
	return f, nil
}

// Count returns the number of entries (saturating at maxCanaryEntries) and whether the scan really
// happened. Any error, a missing directory and a symlink in the path included, is unobserved.
func (f *FileCanaryScan) Count() (int, bool) {
	fd, err := (&OSDecoyFS{}).walk(f.dir, false)
	if err != nil {
		return 0, false
	}
	d := os.NewFile(uintptr(fd), f.dir)
	defer d.Close()
	names, err := d.Readdirnames(maxCanaryEntries + 1)
	if err != nil && len(names) == 0 && !errors.Is(err, io.EOF) {
		return 0, false
	}
	if len(names) > maxCanaryEntries {
		return maxCanaryEntries, true
	}
	return len(names), true
}
