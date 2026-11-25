package hygiene

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ADR-026 section 8 (S1): the rename leaves no old module path behind in Go files, go.mod or
// workflows, and no legacy BRIDGE_ environment name in production Go code outside the places that
// are meant to hold one.
func walkText(t *testing.T, visit func(path, text string)) {
	t.Helper()
	root := filepath.Join("..", "..")
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if d.IsDir() {
			if n := d.Name(); path != root && (n == ".git" || n == "vendor" || n == "testdata") {
				return filepath.SkipDir
			}
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		isWorkflow := strings.HasPrefix(filepath.ToSlash(rel), ".github/workflows/")
		if !strings.HasSuffix(path, ".go") && rel != "go.mod" && !isWorkflow {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		visit(filepath.ToSlash(rel), string(b))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestNoOldModulePathLeftBehind(t *testing.T) {
	old := "nevzatcirak/" + "harness-bridge"
	files := 0
	walkText(t, func(path, text string) {
		files++
		if strings.Contains(text, old) {
			t.Errorf("%s still names the old module path %q", path, old)
		}
	})
	if files < 100 {
		t.Fatalf("only %d files were scanned: the walk is broken", files)
	}
}

func TestLegacyEnvNamesOnlyWhereTheyBelong(t *testing.T) {
	allowed := map[string]bool{
		"cmd/cirak/env.go":       true, // the shim: the legacy prefix
		"cmd/mcpsmoke/main.go":   true, // the dev tool falls back to BRIDGE_TOKEN (Q18)
		"internal/mcp/server.go": true, // tool descriptions are the contract and stay byte-identical
	}
	legacy := "BRIDGE" + "_"
	walkText(t, func(path, text string) {
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") || allowed[path] {
			return
		}
		if strings.Contains(text, legacy) {
			t.Errorf("%s names a legacy %s* variable: production code reads CIRAK_* only (the shim maps the legacy names)", path, legacy)
		}
	})
}
