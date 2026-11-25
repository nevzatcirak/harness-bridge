package releasecheck

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// The release gates (ADR-026 sections 3.1, 3.2 and 8): scripts/release/*.sh are the checks the workflows run.
// These tests run them against a throwaway git repository that holds a tiny Go program, so every gate is
// exercised for real (go build, go version -m, tar, sha256) with no GoReleaser, no network and no signing.

type gateRepo struct {
	t    *testing.T
	dir  string
	rev  string
	dist string
}

func script(t *testing.T, name string) string {
	t.Helper()
	p := filepath.Join(repoRoot(t), "scripts", "release", name)
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return p
}

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.invalid", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// newGateRepo makes a one-commit git repository with a Go module whose main package is ./cmd/cirak.
func newGateRepo(t *testing.T) *gateRepo {
	t.Helper()
	dir := t.TempDir()
	write := func(rel, body string) {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module example.invalid/rel\n\ngo 1.25.0\n")
	write("cmd/cirak/main.go", "package main\n\nimport \"fmt\"\n\nfunc main() { fmt.Println(\"cirak fixture\") }\n")
	write(".gitignore", "dist/\n")
	gitIn(t, dir, "init", "-q", "-b", "main")
	gitIn(t, dir, "add", "-A")
	gitIn(t, dir, "commit", "-q", "-m", "fixture")
	g := &gateRepo{t: t, dir: dir, rev: gitIn(t, dir, "rev-parse", "HEAD"), dist: filepath.Join(dir, "dist")}
	if err := os.MkdirAll(g.dist, 0o755); err != nil {
		t.Fatal(err)
	}
	return g
}

// sh runs a script of scripts/release with the repository as the working directory.
func (g *gateRepo) sh(name string, env []string, args ...string) (string, int) {
	g.t.Helper()
	cmd := exec.Command("sh", append([]string{script(g.t, name)}, args...)...)
	cmd.Dir = g.dir
	cmd.Env = append(append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null"), env...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		ee, ok := err.(*exec.ExitError)
		if !ok {
			g.t.Fatalf("%s: %v", name, err)
		}
		return string(out), ee.ExitCode()
	}
	return string(out), 0
}

func (g *gateRepo) plain(goos, goarch string) string {
	g.t.Helper()
	out := filepath.Join(g.t.TempDir(), "cirak")
	if text, code := g.sh("plain-build.sh", []string{"CIRAK_MAIN=./cmd/cirak"}, goos, goarch, out); code != 0 {
		g.t.Fatalf("plain-build.sh %s %s: exit %d\n%s", goos, goarch, code, text)
	}
	return out
}

// archive packs a binary as dist/cirak_<os>_<arch>.tar.gz (the GoReleaser layout of ADR-026) and returns its path.
func (g *gateRepo) archive(goos, goarch string, binary []byte) string {
	g.t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, f := range []struct {
		name string
		mode int64
		data []byte
	}{{"cirak", 0o755, binary}, {"LICENSE", 0o644, []byte("license\n")}} {
		if err := tw.WriteHeader(&tar.Header{Name: f.name, Mode: f.mode, Size: int64(len(f.data)), Typeflag: tar.TypeReg}); err != nil {
			g.t.Fatal(err)
		}
		_, _ = tw.Write(f.data)
	}
	_ = tw.Close()
	_ = gz.Close()
	p := filepath.Join(g.dist, fmt.Sprintf("cirak_%s_%s.tar.gz", goos, goarch))
	if err := os.WriteFile(p, buf.Bytes(), 0o644); err != nil {
		g.t.Fatal(err)
	}
	return p
}

// checksums writes dist/checksums.txt for the named files (default: every archive).
func (g *gateRepo) checksums(names ...string) {
	g.t.Helper()
	if len(names) == 0 {
		m, _ := filepath.Glob(filepath.Join(g.dist, "cirak_*.tar.gz"))
		for _, p := range m {
			names = append(names, filepath.Base(p))
		}
	}
	var b strings.Builder
	for _, n := range names {
		data, err := os.ReadFile(filepath.Join(g.dist, n))
		if err != nil {
			g.t.Fatal(err)
		}
		fmt.Fprintf(&b, "%s  %s\n", sum(data), n)
	}
	if err := os.WriteFile(filepath.Join(g.dist, "checksums.txt"), []byte(b.String()), 0o644); err != nil {
		g.t.Fatal(err)
	}
}

func readFile(t *testing.T, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// The plain recipe is deterministic and its binary carries the expected, clean VCS stamp.
func TestGateAcceptsACleanBinaryBuiltWithThePlainRecipe(t *testing.T) {
	g := newGateRepo(t)
	bin := g.plain(runtime.GOOS, runtime.GOARCH)
	if out, code := g.sh("check-binary.sh", nil, bin, g.rev); code != 0 {
		t.Fatalf("a clean binary was refused (exit %d):\n%s", code, out)
	}
	again := g.plain(runtime.GOOS, runtime.GOARCH)
	if !bytes.Equal(readFile(t, bin), readFile(t, again)) {
		t.Error("two plain builds of the same commit differ: the recipe is not reproducible")
	}
}

// ADR-026 section 8: a mutation with an untracked file inside the tree must fail the gate (F-H5-5).
func TestGateFailsWhenTheTreeWasDirtyAtBuildTime(t *testing.T) {
	g := newGateRepo(t)
	if err := os.WriteFile(filepath.Join(g.dir, "scratch.txt"), []byte("untracked\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	bin := g.plain(runtime.GOOS, runtime.GOARCH)
	out, code := g.sh("check-binary.sh", nil, bin, g.rev)
	if code == 0 {
		t.Fatalf("a binary built from a dirty tree was accepted:\n%s", out)
	}
	mustContain(t, out, "vcs.modified")
}

// F-H5-5 inverse: dist/ is ignored by .gitignore, so GoReleaser's output directory does not dirty the build.
func TestGateIgnoresTheDistDirectory(t *testing.T) {
	g := newGateRepo(t)
	if err := os.WriteFile(filepath.Join(g.dist, "leftover"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	bin := g.plain(runtime.GOOS, runtime.GOARCH)
	if out, code := g.sh("check-binary.sh", nil, bin, g.rev); code != 0 {
		t.Fatalf("dist/ made the build dirty (exit %d):\n%s", code, out)
	}
}

func TestGateFailsOnAWrongRevisionOrWithoutTrimpath(t *testing.T) {
	g := newGateRepo(t)
	bin := g.plain(runtime.GOOS, runtime.GOARCH)
	if out, code := g.sh("check-binary.sh", nil, bin, strings.Repeat("0", 40)); code == 0 {
		t.Errorf("a wrong revision was accepted:\n%s", out)
	} else {
		mustContain(t, out, "vcs.revision")
	}
	// built without -trimpath: the paths of the build machine are in the binary
	loose := filepath.Join(t.TempDir(), "cirak")
	cmd := exec.Command("go", "build", "-buildvcs=true", "-o", loose, "./cmd/cirak")
	cmd.Dir = g.dir
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	if out, code := g.sh("check-binary.sh", nil, loose, g.rev); code == 0 {
		t.Errorf("a binary built without -trimpath was accepted:\n%s", out)
	} else {
		mustContain(t, out, "trimpath")
	}
	if out, code := g.sh("check-binary.sh", nil); code == 0 {
		t.Errorf("no argument was accepted:\n%s", out)
	}
	if out, code := g.sh("check-binary.sh", nil, filepath.Join(t.TempDir(), "missing"), g.rev); code == 0 {
		t.Errorf("a missing binary was accepted:\n%s", out)
	}
}

// ADR-026 section 3.1: the GoReleaser binary inside the archive must be byte-identical to the plain recipe's;
// every archive must be covered by checksums.txt and stamped clean.
func TestGateAcceptsArchivesThatMatchThePlainRecipe(t *testing.T) {
	g := newGateRepo(t)
	g.archive(runtime.GOOS, runtime.GOARCH, readFile(t, g.plain(runtime.GOOS, runtime.GOARCH)))
	g.archive("darwin", "arm64", readFile(t, g.plain("darwin", "arm64")))
	g.checksums()
	out, code := g.sh("check-archives.sh", []string{"CIRAK_EQUALITY=all"}, g.dist, g.rev)
	if code != 0 {
		t.Fatalf("matching archives were refused (exit %d):\n%s", code, out)
	}
	mustContain(t, out, "cirak_darwin_arm64.tar.gz", "cirak_"+runtime.GOOS+"_"+runtime.GOARCH+".tar.gz")
}

func TestGateFailsOnABinaryThatDiffersByOneByte(t *testing.T) {
	g := newGateRepo(t)
	bin := readFile(t, g.plain(runtime.GOOS, runtime.GOARCH))
	// a byte inside the code: the build info stays readable, so only the equality gate can see the difference
	bin[len(bin)/3] ^= 0x01
	g.archive(runtime.GOOS, runtime.GOARCH, bin)
	g.checksums()
	out, code := g.sh("check-archives.sh", []string{"CIRAK_EQUALITY=all"}, g.dist, g.rev)
	if code == 0 {
		t.Fatalf("a one-byte difference passed the equality gate:\n%s", out)
	}
	mustContain(t, out, "differs from the plain recipe")
}

func TestGateFailsWhenChecksumsDoNotCoverEveryArchive(t *testing.T) {
	g := newGateRepo(t)
	g.archive(runtime.GOOS, runtime.GOARCH, readFile(t, g.plain(runtime.GOOS, runtime.GOARCH)))
	g.archive("darwin", "arm64", readFile(t, g.plain("darwin", "arm64")))
	g.checksums("cirak_" + runtime.GOOS + "_" + runtime.GOARCH + ".tar.gz")
	out, code := g.sh("check-archives.sh", []string{"CIRAK_EQUALITY=all"}, g.dist, g.rev)
	if code == 0 {
		t.Fatalf("an archive missing from checksums.txt was accepted:\n%s", out)
	}
	mustContain(t, out, "checksums.txt does not list")
	// and a wrong digest
	g.checksums()
	cs := string(readFile(t, filepath.Join(g.dist, "checksums.txt")))
	cs = strings.Replace(cs, cs[:1], map[bool]string{true: "1", false: "0"}[cs[:1] == "0"], 1)
	_ = os.WriteFile(filepath.Join(g.dist, "checksums.txt"), []byte(cs), 0o644)
	if out, code := g.sh("check-archives.sh", []string{"CIRAK_EQUALITY=all"}, g.dist, g.rev); code == 0 {
		t.Errorf("a wrong digest in checksums.txt was accepted:\n%s", out)
	}
	// no checksums file at all
	_ = os.Remove(filepath.Join(g.dist, "checksums.txt"))
	if out, code := g.sh("check-archives.sh", []string{"CIRAK_EQUALITY=all"}, g.dist, g.rev); code == 0 {
		t.Errorf("a missing checksums.txt was accepted:\n%s", out)
	}
}

func TestGateFailsOnADirtyBinaryInsideAnArchive(t *testing.T) {
	g := newGateRepo(t)
	if err := os.WriteFile(filepath.Join(g.dir, "scratch.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	g.archive(runtime.GOOS, runtime.GOARCH, readFile(t, g.plain(runtime.GOOS, runtime.GOARCH)))
	g.checksums()
	out, code := g.sh("check-archives.sh", []string{"CIRAK_EQUALITY=none"}, g.dist, g.rev)
	if code == 0 {
		t.Fatalf("a dirty-tree binary inside an archive was accepted:\n%s", out)
	}
	mustContain(t, out, "vcs.modified")
}

func TestGateFailsOnAnEmptyDistOrAMissingBinaryMember(t *testing.T) {
	g := newGateRepo(t)
	if out, code := g.sh("check-archives.sh", nil, g.dist, g.rev); code == 0 {
		t.Errorf("an empty dist directory was accepted:\n%s", out)
	}
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	_ = tw.WriteHeader(&tar.Header{Name: "LICENSE", Mode: 0o644, Size: 2, Typeflag: tar.TypeReg})
	_, _ = tw.Write([]byte("x\n"))
	_ = tw.Close()
	_ = gz.Close()
	_ = os.WriteFile(filepath.Join(g.dist, "cirak_linux_amd64.tar.gz"), buf.Bytes(), 0o644)
	g.checksums()
	if out, code := g.sh("check-archives.sh", []string{"CIRAK_EQUALITY=none"}, g.dist, g.rev); code == 0 {
		t.Errorf("an archive without the cirak binary was accepted:\n%s", out)
	}
}

// ADR-026 section 3.2 (2): the independent rebuild compares the sha256 of every binary with the archives and
// fails on a one-byte difference. It does not use GoReleaser.
func TestIndependentRebuildMatchesEveryArchiveAndFailsOnOneByte(t *testing.T) {
	g := newGateRepo(t)
	g.archive("linux", "amd64", readFile(t, g.plain("linux", "amd64")))
	g.archive("linux", "arm64", readFile(t, g.plain("linux", "arm64")))
	g.archive("darwin", "amd64", readFile(t, g.plain("darwin", "amd64")))
	g.archive("darwin", "arm64", readFile(t, g.plain("darwin", "arm64")))
	g.checksums()
	out, code := g.sh("reproduce.sh", nil, g.dist)
	if code != 0 {
		t.Fatalf("an exact match was refused (exit %d):\n%s", code, out)
	}
	for _, target := range []string{"linux_amd64", "linux_arm64", "darwin_amd64", "darwin_arm64"} {
		mustContain(t, out, target)
	}
	mustContain(t, out, "reproducible")

	b := readFile(t, g.plain("darwin", "arm64"))
	b[len(b)/2] ^= 0x01
	g.archive("darwin", "arm64", b)
	g.checksums()
	out, code = g.sh("reproduce.sh", nil, g.dist)
	if code == 0 {
		t.Fatalf("a one-byte difference passed the independent rebuild:\n%s", out)
	}
	mustContain(t, out, "MISMATCH", "darwin_arm64")
	if out, code := g.sh("reproduce.sh", nil, filepath.Join(t.TempDir(), "empty")); code == 0 {
		t.Errorf("a missing dist directory was accepted:\n%s", out)
	}
}

// The scripts use no GoReleaser and no network.
func TestGateScriptsUseNoGoReleaserAndNoNetwork(t *testing.T) {
	for _, name := range []string{"plain-build.sh", "check-binary.sh", "check-archives.sh", "reproduce.sh"} {
		src := read(t, "scripts/release/"+name)
		if !strings.HasPrefix(src, "#!/bin/sh\n") || !strings.Contains(src, "set -eu") {
			t.Errorf("%s must be POSIX sh with set -eu", name)
		}
		for _, forbidden := range []string{"goreleaser", "curl", "wget", "eval ", "sudo", "git push", "git tag"} {
			if strings.Contains(src, forbidden) {
				t.Errorf("%s contains %q", name, forbidden)
			}
		}
	}
}
