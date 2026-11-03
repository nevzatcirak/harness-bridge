//go:build linux

package execution

import (
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
)

func TestOwnershipAcrossProcesses(t *testing.T) {
	if dir := os.Getenv("HB_OWNERSHIP_TEST_DIR"); dir != "" {
		o, err := AcquireOwnership(dir, []string{"ses_test"})
		if err == nil {
			o.Close()
			os.Exit(2)
		}
		os.Exit(0)
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	o, err := AcquireOwnership(dir, []string{"ses_test", "ses_test"})
	if err != nil {
		t.Fatal(err)
	}
	defer o.Close()
	cmd := exec.Command(os.Args[0], "-test.run=^TestOwnershipAcrossProcesses$")
	cmd.Env = append(os.Environ(), "HB_OWNERSHIP_TEST_DIR="+dir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("second process acquired writer: %v %s", err, out)
	}
	if _, err := AcquireOwnership(dir, []string{"ses_other", "ses_test"}); err == nil {
		t.Fatal("overlapping allowlist accepted")
	}
	other, err := AcquireOwnership(dir, []string{"ses_other"})
	if err != nil {
		t.Fatal("partial acquisition leaked", err)
	}
	other.Close()
	o.Close()
	recovered, err := AcquireOwnership(dir, []string{"ses_test"})
	if err != nil {
		t.Fatal(err)
	}
	recovered.Close()
}

func TestOwnershipRejectsUnsafeFiles(t *testing.T) {
	for _, kind := range []string{"missing", "relative", "public-dir", "symlink-dir", "symlink-file", "public-file", "hardlink", "fifo"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.Chmod(dir, 0700); err != nil {
				t.Fatal(err)
			}
			name := filepath.Join(dir, fmt.Sprintf("%x.lock", sha256.Sum256([]byte("ses_test"))))
			switch kind {
			case "missing":
				dir = filepath.Join(dir, "missing")
			case "relative":
				dir = "."
			case "public-dir":
				if err := os.Chmod(dir, 0755); err != nil {
					t.Fatal(err)
				}
			case "symlink-dir":
				link := filepath.Join(t.TempDir(), "link")
				if err := os.Symlink(dir, link); err != nil {
					t.Fatal(err)
				}
				dir = link
			case "symlink-file":
				if err := os.Symlink(filepath.Join(dir, "target"), name); err != nil {
					t.Fatal(err)
				}
			case "public-file":
				if err := os.WriteFile(name, nil, 0644); err != nil {
					t.Fatal(err)
				}
			case "hardlink":
				target := filepath.Join(dir, "target")
				if err := os.WriteFile(target, nil, 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Link(target, name); err != nil {
					t.Fatal(err)
				}
			case "fifo":
				if err := syscall.Mkfifo(name, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if o, err := AcquireOwnership(dir, []string{"ses_test"}); err == nil {
				o.Close()
				t.Fatal("unsafe ownership accepted")
			}
		})
	}
}
