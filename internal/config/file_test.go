package config

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeConfig(t *testing.T, dir, body string, mode os.FileMode) string {
	t.Helper()
	p := filepath.Join(dir, "config.json")
	if err := os.WriteFile(p, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, mode); err != nil {
		t.Fatal(err)
	}
	return p
}

func privateDir(t *testing.T) string {
	t.Helper()
	d, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(d, 0o700); err != nil {
		t.Fatal(err)
	}
	return d
}

func uid() uint32 { return uint32(os.Geteuid()) }

func TestLoadAcceptsAPrivateFile(t *testing.T) {
	d := privateDir(t)
	p := writeConfig(t, d, `{"server":{"listen":"127.0.0.1:9"}}`, 0o600)
	f, err := Load(p, uid())
	if err != nil || f.EnvForm("server.listen") != "127.0.0.1:9" {
		t.Fatalf("%v", err)
	}
}

func TestLoadFileRules(t *testing.T) {
	body := `{"server":{"listen":"127.0.0.1:9"}}`
	for _, mode := range []os.FileMode{0o644, 0o640, 0o660, 0o606, 0o666, 0o400, 0o700, os.ModeSetgid | 0o600, os.ModeSetuid | 0o600, os.ModeSticky | 0o600} {
		d := privateDir(t)
		p := writeConfig(t, d, body, mode)
		if _, err := Load(p, uid()); err == nil || !strings.Contains(err.Error(), "mode 0600") {
			t.Errorf("mode %o: %v", mode, err)
		}
	}
	t.Run("owner", func(t *testing.T) {
		p := writeConfig(t, privateDir(t), body, 0o600)
		if _, err := Load(p, uid()+1); err == nil || !strings.Contains(err.Error(), "owned by the process user") {
			t.Fatalf("%v", err)
		}
	})
	t.Run("relative", func(t *testing.T) {
		if _, err := Load("config.json", uid()); err == nil || !strings.Contains(err.Error(), "absolute and canonical") {
			t.Fatalf("%v", err)
		}
	})
	t.Run("not canonical", func(t *testing.T) {
		d := privateDir(t)
		p := writeConfig(t, d, body, 0o600)
		if _, err := Load(d+"/sub/../config.json", uid()); err == nil {
			t.Fatal("a path with .. accepted")
		}
		if _, err := Load(p+"/", uid()); err == nil {
			t.Fatal("a path with a trailing slash accepted")
		}
	})
	t.Run("symlink file", func(t *testing.T) {
		d := privateDir(t)
		real := writeConfig(t, d, body, 0o600)
		link := filepath.Join(d, "link.json")
		if err := os.Symlink(real, link); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(link, uid()); err == nil || !strings.Contains(err.Error(), "symbolic link") {
			t.Fatalf("%v", err)
		}
	})
	t.Run("symlink directory component", func(t *testing.T) {
		d := privateDir(t)
		real := filepath.Join(d, "real")
		if err := os.Mkdir(real, 0o700); err != nil {
			t.Fatal(err)
		}
		writeConfig(t, real, body, 0o600)
		link := filepath.Join(d, "linkdir")
		if err := os.Symlink(real, link); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(filepath.Join(link, "config.json"), uid()); err == nil || !strings.Contains(err.Error(), "symbolic link") {
			t.Fatalf("%v", err)
		}
	})
	t.Run("not regular", func(t *testing.T) {
		d := privateDir(t)
		p := filepath.Join(d, "config.json")
		if err := os.Mkdir(p, 0o700); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(p, uid()); err == nil {
			t.Fatal("a directory accepted")
		}
	})
	t.Run("parent writable", func(t *testing.T) {
		for _, m := range []os.FileMode{0o770, 0o707, 0o777} {
			d := privateDir(t)
			p := writeConfig(t, d, body, 0o600)
			if err := os.Chmod(d, m); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(p, uid()); err == nil || !strings.Contains(err.Error(), "group or world writable") {
				t.Errorf("parent %o: %v", m, err)
			}
		}
		// a group- or world-READABLE parent is fine: only write access is the hole
		d := privateDir(t)
		p := writeConfig(t, d, body, 0o600)
		if err := os.Chmod(d, 0o755); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(p, uid()); err != nil {
			t.Errorf("a 0755 parent refused: %v", err)
		}
	})
	t.Run("missing", func(t *testing.T) {
		if _, err := Load(filepath.Join(privateDir(t), "nope.json"), uid()); err == nil {
			t.Fatal("a missing file accepted")
		}
	})
	t.Run("over 1 MiB", func(t *testing.T) {
		d := privateDir(t)
		p := writeConfig(t, d, `{"server":{"listen":"`+strings.Repeat("a", MaxFileSize)+`"}}`, 0o600)
		if _, err := Load(p, uid()); err == nil || !strings.Contains(err.Error(), "1 MiB") {
			t.Fatalf("%v", err)
		}
	})
	t.Run("parse errors name the file", func(t *testing.T) {
		d := privateDir(t)
		p := writeConfig(t, d, `{"nope":1}`, 0o600)
		if _, err := Load(p, uid()); err == nil || !strings.Contains(err.Error(), p) {
			t.Fatalf("%v", err)
		}
	})
}

func TestExists(t *testing.T) {
	d := privateDir(t)
	if ok, err := Exists(filepath.Join(d, "nope")); ok || err != nil {
		t.Fatalf("%v %v", ok, err)
	}
	p := writeConfig(t, d, "{}", 0o600)
	if ok, err := Exists(p); !ok || err != nil {
		t.Fatalf("%v %v", ok, err)
	}
	// a dangling symlink "exists", so the file rules refuse it instead of discovery skipping it
	link := filepath.Join(d, "dangling")
	if err := os.Symlink(filepath.Join(d, "gone"), link); err != nil {
		t.Fatal(err)
	}
	if ok, err := Exists(link); !ok || err != nil {
		t.Fatalf("%v %v", ok, err)
	}
}

// #526 P3-2: the os.SameFile re-check of ReadChecked cannot be exercised by a deterministic swap without a
// hook inside production code, so it is pinned structurally: ReadChecked must compare the inspected entry
// with the opened descriptor, and refuse on a mismatch.
func TestReadCheckedComparesTheOpenedFileWithTheInspectedOne(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "file.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var found, refuses bool
	for _, d := range f.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok || fd.Name.Name != "ReadChecked" {
			continue
		}
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			ifs, ok := n.(*ast.IfStmt)
			if !ok {
				return true
			}
			hasSame := false
			ast.Inspect(ifs.Cond, func(m ast.Node) bool {
				if sel, ok := m.(*ast.SelectorExpr); ok && sel.Sel.Name == "SameFile" {
					hasSame = true
				}
				return true
			})
			if hasSame {
				found = true
				for _, st := range ifs.Body.List {
					if _, ok := st.(*ast.ReturnStmt); ok {
						refuses = true
					}
				}
			}
			return true
		})
	}
	if !found || !refuses {
		t.Fatal("ReadChecked does not refuse when os.SameFile says the opened file is not the inspected one")
	}
}
