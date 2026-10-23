package security

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPolicy(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	p, e := New([]string{root}, []string{"ses_allowed"})
	if e != nil {
		t.Fatal(e)
	}
	if !p.Allows("ses_allowed", root) || p.Allows("ses_other", root) || p.Allows("ses_allowed", outside) || p.Allows("ses_allowed", "relative") {
		t.Fatal("policy failure")
	}
	link := filepath.Join(root, "escape")
	if e = os.Symlink(outside, link); e != nil {
		t.Fatal(e)
	}
	if p.Allows("ses_allowed", link) {
		t.Fatal("symlink escaped")
	}
	if _, e = New(nil, nil); e == nil {
		t.Fatal("empty policy")
	}
}
