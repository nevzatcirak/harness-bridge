package releasecheck

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// The install.sh tests (ADR-026 section 8, S2): every test runs the script in a temporary directory with a
// local fixture release and a PATH that holds only fake or symlinked tools. Nothing is downloaded; the fake curl
// serves files from the fixture and records what it was asked for, so "no network" is an assertion, not a hope.

type installEnv struct {
	t       *testing.T
	root    string
	home    string
	tmpdir  string
	bin     string
	fixture string
	extra   []string // extra environment entries
}

var realTools = []string{"awk", "cat", "chmod", "cp", "cut", "dirname", "basename", "grep", "gzip", "head", "ls", "mkdir", "mktemp", "mv", "rm", "sed", "sort", "tar", "tr", "wc", "touch", "cmp", "sleep"}

func newInstallEnv(t *testing.T) *installEnv {
	t.Helper()
	root := t.TempDir()
	e := &installEnv{t: t, root: root, home: filepath.Join(root, "home"), tmpdir: filepath.Join(root, "tmp"), bin: filepath.Join(root, "bin"), fixture: filepath.Join(root, "fixture")}
	for _, d := range []string{e.home, e.tmpdir, e.bin, e.fixture} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, tool := range realTools {
		p, err := exec.LookPath(tool)
		if err != nil {
			t.Fatalf("test needs %s on the PATH: %v", tool, err)
		}
		if err := os.Symlink(p, filepath.Join(e.bin, tool)); err != nil {
			t.Fatal(err)
		}
	}
	e.useSHA("sha256sum")
	e.fake("uname", `case "$1" in -s) echo "${FAKE_OS:-Linux}" ;; -m) echo "${FAKE_ARCH:-x86_64}" ;; *) echo Linux ;; esac`)
	e.fake("id", `echo "${FAKE_UID:-1000}"`)
	e.fake("sudo", `printf '%s\n' "$*" >> "$SUDO_LOG"; exit 0`)
	e.fake("curl", `printf '%s\n' "$*" >> "$CURL_LOG"
out=""; url=""
while [ $# -gt 0 ]; do case "$1" in -o) out="$2"; shift ;; --proto|--tlsv1.2) ;; -*) ;; *) url="$1" ;; esac; shift; done
name=$(basename "$url")
[ -f "$FIXTURE_DIR/$name" ] || { echo "fake curl: 404 $url" >&2; exit 22; }
cp "$FIXTURE_DIR/$name" "$out"`)
	return e
}

// fake writes an executable shell script into the tool directory.
func (e *installEnv) fake(name, body string) {
	e.t.Helper()
	p := filepath.Join(e.bin, name)
	_ = os.Remove(p)
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		e.t.Fatal(err)
	}
}

// useSHA exposes exactly one of the two checksum tools (or none, for "").
func (e *installEnv) useSHA(which string) {
	e.t.Helper()
	_ = os.Remove(filepath.Join(e.bin, "sha256sum"))
	_ = os.Remove(filepath.Join(e.bin, "shasum"))
	switch which {
	case "sha256sum":
		p, err := exec.LookPath("sha256sum")
		if err != nil {
			e.t.Fatal(err)
		}
		_ = os.Symlink(p, filepath.Join(e.bin, "sha256sum"))
	case "shasum":
		e.fake("shasum", `[ "$1" = "-a" ] && [ "$2" = "256" ] || { echo "shasum: only -a 256" >&2; exit 2; }
shift 2; sha256sum "$@"`)
		// shasum is implemented through the real sha256sum, which must therefore be reachable by absolute path
		p, err := exec.LookPath("sha256sum")
		if err != nil {
			e.t.Fatal(err)
		}
		e.fake("shasum", fmt.Sprintf(`[ "$1" = "-a" ] && [ "$2" = "256" ] || { echo "shasum: only -a 256" >&2; exit 2; }
shift 2; exec %s "$@"`, p))
	}
}

func sum(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

// release writes a fixture release for one platform: the archive, checksums.txt and (optionally) a bundle.
// It returns the archive bytes.
func (e *installEnv) release(version, goos, goarch string, bundle bool) []byte {
	e.t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	add := func(name string, mode int64, data string) {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: mode, Size: int64(len(data)), Typeflag: tar.TypeReg}); err != nil {
			e.t.Fatal(err)
		}
		if _, err := tw.Write([]byte(data)); err != nil {
			e.t.Fatal(err)
		}
	}
	add("cirak", 0o755, fmt.Sprintf("#!/bin/sh\necho 'cirak %s'\necho 'revision fixture'\n", version))
	add("LICENSE", 0o644, "license\n")
	add("README.md", 0o644, "readme\n")
	if err := tw.Close(); err != nil {
		e.t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		e.t.Fatal(err)
	}
	archive := fmt.Sprintf("cirak_%s_%s.tar.gz", goos, goarch)
	e.put(archive, buf.Bytes())
	e.put("checksums.txt", []byte(fmt.Sprintf("%s  %s\n%s  install.sh\n", sum(buf.Bytes()), archive, strings.Repeat("0", 64))))
	if bundle {
		e.put("checksums.txt.sigstore.json", []byte("{\"bundle\":true}\n"))
	}
	return buf.Bytes()
}

func (e *installEnv) put(name string, data []byte) {
	e.t.Helper()
	if err := os.WriteFile(filepath.Join(e.fixture, name), data, 0o644); err != nil {
		e.t.Fatal(err)
	}
}

// run executes install.sh with the controlled environment and returns the combined output and the exit code.
func (e *installEnv) run(args ...string) (string, int) {
	e.t.Helper()
	sh, err := exec.LookPath("sh")
	if err != nil {
		e.t.Fatal(err)
	}
	cmd := exec.Command(sh, append([]string{filepath.Join(repoRoot(e.t), "install.sh")}, args...)...)
	cmd.Dir = e.root
	cmd.Env = append([]string{
		"PATH=" + e.bin, "HOME=" + e.home, "TMPDIR=" + e.tmpdir, "LC_ALL=C",
		"FIXTURE_DIR=" + e.fixture, "SUDO_LOG=" + filepath.Join(e.root, "sudo.log"), "CURL_LOG=" + filepath.Join(e.root, "curl.log"),
	}, e.extra...)
	out, err := cmd.CombinedOutput()
	code := 0
	if err != nil {
		ee, ok := err.(*exec.ExitError)
		if !ok {
			e.t.Fatalf("running install.sh: %v", err)
		}
		code = ee.ExitCode()
	}
	return string(out), code
}

func (e *installEnv) installed() string { return filepath.Join(e.home, ".local", "bin", "cirak") }

func (e *installEnv) exists(rel string) bool {
	_, err := os.Lstat(filepath.Join(e.root, rel))
	return err == nil
}

// tree lists every path under the temporary root except the fixture and the tool directory, so a run that
// wrote somewhere unexpected shows up as a difference.
func (e *installEnv) tree() []string {
	var out []string
	_ = filepath.WalkDir(e.root, func(p string, d fs.DirEntry, err error) error {
		rel, _ := filepath.Rel(e.root, p)
		if rel == "." || rel == "fixture" || rel == "bin" || strings.HasPrefix(rel, "fixture/") || strings.HasPrefix(rel, "bin/") {
			return nil
		}
		out = append(out, rel)
		return nil
	})
	sort.Strings(out)
	return out
}

func mustContain(t *testing.T, out string, wants ...string) {
	t.Helper()
	for _, w := range wants {
		if !strings.Contains(out, w) {
			t.Errorf("output lacks %q:\n%s", w, out)
		}
	}
}

func TestInstallFromDirInstallsTheVerifiedBinaryWithoutAnyRequest(t *testing.T) {
	e := newInstallEnv(t)
	e.release("v0.1.0", "linux", "amd64", false)
	out, code := e.run("--from-dir", e.fixture)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	mustContain(t, out, "cirak v0.1.0 installed to "+e.installed())
	info, err := os.Stat(e.installed())
	if err != nil || info.Mode().Perm() != 0o755 {
		t.Fatalf("installed binary: %v %v", info, err)
	}
	if e.exists("curl.log") {
		t.Error("--from-dir made a request (the fake curl was called)")
	}
	if e.exists("sudo.log") {
		t.Error("sudo was used by a default install")
	}
	if got := e.tree(); strings.Join(got, ",") != "home,home/.local,home/.local/bin,home/.local/bin/cirak,tmp" {
		t.Errorf("the install wrote outside the target and a temporary directory: %v", got)
	}
}

func TestInstallRefusesABadChecksum(t *testing.T) {
	e := newInstallEnv(t)
	e.release("v0.1.0", "linux", "amd64", false)
	e.put("checksums.txt", []byte(strings.Repeat("a", 64)+"  cirak_linux_amd64.tar.gz\n"))
	out, code := e.run("--from-dir", e.fixture)
	if code == 0 || e.exists("home/.local/bin/cirak") {
		t.Fatalf("a bad checksum was accepted (exit %d):\n%s", code, out)
	}
	mustContain(t, out, "checksum mismatch")
}

func TestInstallRefusesATamperedArchive(t *testing.T) {
	e := newInstallEnv(t)
	archive := e.release("v0.1.0", "linux", "amd64", false)
	tampered := append([]byte(nil), archive...)
	tampered[len(tampered)/2] ^= 0xff
	e.put("cirak_linux_amd64.tar.gz", tampered)
	out, code := e.run("--from-dir", e.fixture)
	if code == 0 || e.exists("home/.local/bin/cirak") {
		t.Fatalf("a tampered archive was accepted (exit %d):\n%s", code, out)
	}
	mustContain(t, out, "checksum mismatch")
}

func TestInstallRefusesAnArchiveThatChecksumsDoNotCover(t *testing.T) {
	e := newInstallEnv(t)
	e.release("v0.1.0", "linux", "amd64", false)
	e.put("checksums.txt", []byte(strings.Repeat("a", 64)+"  something_else.tar.gz\n"))
	out, code := e.run("--from-dir", e.fixture)
	if code == 0 || e.exists("home/.local/bin/cirak") {
		t.Fatalf("an uncovered archive was accepted (exit %d):\n%s", code, out)
	}
	mustContain(t, out, "does not list")
}

func TestInstallRefusesAnUnsupportedPlatform(t *testing.T) {
	for _, tc := range []struct{ os, arch string }{{"FreeBSD", "x86_64"}, {"Linux", "riscv64"}, {"MINGW64_NT-10.0", "x86_64"}, {"Darwin", "ppc64"}} {
		e := newInstallEnv(t)
		e.release("v0.1.0", "linux", "amd64", false)
		e.extra = []string{"FAKE_OS=" + tc.os, "FAKE_ARCH=" + tc.arch}
		out, code := e.run("--from-dir", e.fixture)
		if code == 0 || e.exists("home/.local/bin/cirak") {
			t.Errorf("%s/%s was accepted (exit %d):\n%s", tc.os, tc.arch, code, out)
			continue
		}
		mustContain(t, out, "unsupported platform", "linux/amd64", "linux/arm64", "darwin/amd64", "darwin/arm64")
	}
}

func TestInstallMapsTheSupportedPlatforms(t *testing.T) {
	for _, tc := range []struct{ os, arch, goos, goarch string }{
		{"Linux", "x86_64", "linux", "amd64"}, {"Linux", "aarch64", "linux", "arm64"},
		{"Darwin", "x86_64", "darwin", "amd64"}, {"Darwin", "arm64", "darwin", "arm64"},
	} {
		e := newInstallEnv(t)
		e.release("v0.1.0", tc.goos, tc.goarch, false)
		e.extra = []string{"FAKE_OS=" + tc.os, "FAKE_ARCH=" + tc.arch}
		out, code := e.run("--from-dir", e.fixture)
		if code != 0 {
			t.Errorf("%s/%s: exit %d:\n%s", tc.os, tc.arch, code, out)
		}
	}
}

func TestInstallNeedsASHA256Tool(t *testing.T) {
	e := newInstallEnv(t)
	e.release("v0.1.0", "linux", "amd64", false)
	e.useSHA("")
	out, code := e.run("--from-dir", e.fixture)
	if code == 0 || e.exists("home/.local/bin/cirak") {
		t.Fatalf("no sha256sum and no shasum must be an error (exit %d):\n%s", code, out)
	}
	mustContain(t, out, "no sha256 tool")
}

func TestInstallFallsBackToShasum(t *testing.T) {
	e := newInstallEnv(t)
	e.release("v0.1.0", "linux", "amd64", false)
	e.useSHA("shasum")
	out, code := e.run("--from-dir", e.fixture)
	if code != 0 {
		t.Fatalf("shasum -a 256 fallback: exit %d:\n%s", code, out)
	}
}

func TestInstallIsIdempotentAndReplacesAnotherVersion(t *testing.T) {
	e := newInstallEnv(t)
	e.release("v0.1.0", "linux", "amd64", false)
	if out, code := e.run("--from-dir", e.fixture); code != 0 {
		t.Fatalf("first run: exit %d:\n%s", code, out)
	}
	before, _ := os.Stat(e.installed())
	out, code := e.run("--from-dir", e.fixture)
	if code != 0 {
		t.Fatalf("second run: exit %d:\n%s", code, out)
	}
	mustContain(t, out, "cirak v0.1.0 is already installed")
	after, _ := os.Stat(e.installed())
	if !os.SameFile(before, after) || !before.ModTime().Equal(after.ModTime()) {
		t.Error("the second run of the same version replaced the binary (not a no-op)")
	}
	// another version replaces it through a temporary file and an atomic rename
	e.release("v0.2.0", "linux", "amd64", false)
	out, code = e.run("--from-dir", e.fixture)
	if code != 0 {
		t.Fatalf("upgrade: exit %d:\n%s", code, out)
	}
	mustContain(t, out, "cirak v0.2.0 installed to", "replaced v0.1.0")
	b, _ := os.ReadFile(e.installed())
	if !strings.Contains(string(b), "v0.2.0") {
		t.Errorf("the binary was not replaced: %s", b)
	}
	if got := e.tree(); strings.Join(got, ",") != "home,home/.local,home/.local/bin,home/.local/bin/cirak,tmp" {
		t.Errorf("a temporary file was left behind: %v", got)
	}
}

func TestInstallDryRunWritesNothingAndRequestsNothing(t *testing.T) {
	e := newInstallEnv(t)
	e.release("v0.1.0", "linux", "amd64", false)
	before := e.tree()
	for _, args := range [][]string{{"--dry-run", "--from-dir", e.fixture}, {"--dry-run"}, {"--dry-run", "--version", "v0.1.0", "--system"}} {
		out, code := e.run(args...)
		if code != 0 {
			t.Fatalf("%v: exit %d:\n%s", args, code, out)
		}
		mustContain(t, out, "plan:", "linux/amd64")
		if got := e.tree(); strings.Join(got, ",") != strings.Join(before, ",") {
			t.Errorf("%v wrote something: %v", args, got)
		}
		if e.exists("curl.log") || e.exists("sudo.log") {
			t.Errorf("%v made a request or used sudo", args)
		}
	}
}

func TestInstallSystemIsTheOnlyPathThatUsesSudoAndOnlyForTheCopy(t *testing.T) {
	e := newInstallEnv(t)
	e.release("v0.1.0", "linux", "amd64", false)
	out, code := e.run("--from-dir", e.fixture, "--system")
	if code != 0 {
		t.Fatalf("--system: exit %d:\n%s", code, out)
	}
	log, err := os.ReadFile(filepath.Join(e.root, "sudo.log"))
	if err != nil {
		t.Fatalf("--system did not use sudo: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(log)), "\n")
	if len(lines) != 1 || !strings.HasPrefix(lines[0], "install -m 0755 ") || !strings.HasSuffix(lines[0], " /usr/local/bin/cirak") {
		t.Errorf("sudo must run exactly one command, the copy: %q", lines)
	}
	if e.exists("home/.local/bin/cirak") {
		t.Error("--system also wrote to the user directory")
	}
	// as root there is nothing to elevate: no sudo is run
	e2 := newInstallEnv(t)
	e2.release("v0.1.0", "linux", "amd64", false)
	e2.extra = []string{"FAKE_UID=0"}
	e2.fake("install", `printf '%s\n' "$*" >> "$INSTALL_LOG"; exit 0`)
	e2.extra = append(e2.extra, "INSTALL_LOG="+filepath.Join(e2.root, "install.log"))
	if out, code := e2.run("--from-dir", e2.fixture, "--system"); code != 0 || e2.exists("sudo.log") {
		t.Errorf("as root --system must not use sudo (exit %d):\n%s", code, out)
	}
}

func TestInstallRefusesAMirrorThatIsNotHTTPS(t *testing.T) {
	for _, base := range []string{"http://mirror.example/cirak", "ftp://mirror.example", "file:///tmp/x", "mirror.example/cirak", ""} {
		e := newInstallEnv(t)
		e.release("v0.1.0", "linux", "amd64", false)
		e.extra = []string{"CIRAK_DOWNLOAD_BASE=" + base}
		if base == "" {
			continue // an empty value means "unset": the default base applies
		}
		out, code := e.run()
		if code == 0 {
			t.Errorf("CIRAK_DOWNLOAD_BASE=%q was accepted:\n%s", base, out)
		}
		mustContain(t, out, "must start with https://")
		if e.exists("curl.log") {
			t.Errorf("CIRAK_DOWNLOAD_BASE=%q: a request was made before the refusal", base)
		}
	}
}

func TestInstallDownloadUsesHTTPSOnlyAndTheReleaseLayout(t *testing.T) {
	e := newInstallEnv(t)
	e.release("v0.1.0", "linux", "amd64", true)
	e.extra = []string{"CIRAK_DOWNLOAD_BASE=https://mirror.example/cirak/releases"}
	out, code := e.run("--version", "v0.1.0")
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	log, _ := os.ReadFile(filepath.Join(e.root, "curl.log"))
	for _, want := range []string{
		"--proto =https", "--tlsv1.2",
		"https://mirror.example/cirak/releases/download/v0.1.0/cirak_linux_amd64.tar.gz",
		"https://mirror.example/cirak/releases/download/v0.1.0/checksums.txt",
		"https://mirror.example/cirak/releases/download/v0.1.0/checksums.txt.sigstore.json",
	} {
		if !strings.Contains(string(log), want) {
			t.Errorf("curl log lacks %q:\n%s", want, log)
		}
	}
	for _, l := range strings.Split(strings.TrimSpace(string(log)), "\n") {
		if strings.Contains(l, "http://") {
			t.Errorf("a plain http request was made: %s", l)
		}
	}
	// no version: the latest layout, on the default GitHub base
	e2 := newInstallEnv(t)
	e2.release("v0.1.0", "linux", "amd64", false)
	if out, code := e2.run(); code != 0 {
		t.Fatalf("latest: exit %d:\n%s", code, out)
	}
	log2, _ := os.ReadFile(filepath.Join(e2.root, "curl.log"))
	if !strings.Contains(string(log2), "https://github.com/nevzatcirak/cirak/releases/latest/download/cirak_linux_amd64.tar.gz") {
		t.Errorf("the default download URL is wrong:\n%s", log2)
	}
}

func TestInstallVerifiesTheCosignBundleWhenCosignIsPresent(t *testing.T) {
	e := newInstallEnv(t)
	e.release("v0.1.0", "linux", "amd64", true)
	e.fake("cosign", `printf '%s\n' "$*" >> "$COSIGN_LOG"; exit "${COSIGN_RC:-0}"`)
	e.extra = []string{"COSIGN_LOG=" + filepath.Join(e.root, "cosign.log")}
	if out, code := e.run("--from-dir", e.fixture); code != 0 {
		t.Fatalf("cosign ok: exit %d:\n%s", code, out)
	}
	log, _ := os.ReadFile(filepath.Join(e.root, "cosign.log"))
	for _, want := range []string{"verify-blob", "--bundle", "checksums.txt.sigstore.json", "--certificate-identity-regexp", `nevzatcirak/cirak/\.github/workflows/release\.yml@refs/tags/v`, "--certificate-oidc-issuer https://token.actions.githubusercontent.com"} {
		if !strings.Contains(string(log), want) {
			t.Errorf("cosign was not asked for %q:\n%s", want, log)
		}
	}
	// a failed verification is an error and nothing is installed
	e2 := newInstallEnv(t)
	e2.release("v0.1.0", "linux", "amd64", true)
	e2.fake("cosign", `exit 1`)
	out, code := e2.run("--from-dir", e2.fixture)
	if code == 0 || e2.exists("home/.local/bin/cirak") {
		t.Errorf("a failed cosign verification was accepted (exit %d):\n%s", code, out)
	}
	mustContain(t, out, "cosign verification failed")
}

func TestInstallCosignRequireMakesAMissingCosignAnError(t *testing.T) {
	e := newInstallEnv(t)
	e.release("v0.1.0", "linux", "amd64", true)
	e.extra = []string{"CIRAK_VERIFY=require"}
	out, code := e.run("--from-dir", e.fixture)
	if code == 0 || e.exists("home/.local/bin/cirak") {
		t.Fatalf("CIRAK_VERIFY=require without cosign was accepted (exit %d):\n%s", code, out)
	}
	mustContain(t, out, "CIRAK_VERIFY=require")
	// and a missing bundle with cosign present is an error under require, a warning otherwise
	e2 := newInstallEnv(t)
	e2.release("v0.1.0", "linux", "amd64", false)
	e2.fake("cosign", `exit 0`)
	e2.extra = []string{"CIRAK_VERIFY=require"}
	if out, code := e2.run("--from-dir", e2.fixture); code == 0 {
		t.Errorf("CIRAK_VERIFY=require without a bundle was accepted:\n%s", out)
	}
	e3 := newInstallEnv(t)
	e3.release("v0.1.0", "linux", "amd64", false)
	e3.fake("cosign", `exit 0`)
	if out, code := e3.run("--from-dir", e3.fixture); code != 0 {
		t.Errorf("without a bundle the sha256 check alone must be enough (exit %d):\n%s", code, out)
	}
}

func TestInstallRefusesAnArchiveWithoutTheBinary(t *testing.T) {
	e := newInstallEnv(t)
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	_ = tw.WriteHeader(&tar.Header{Name: "LICENSE", Mode: 0o644, Size: 2, Typeflag: tar.TypeReg})
	_, _ = tw.Write([]byte("x\n"))
	_ = tw.Close()
	_ = gz.Close()
	e.put("cirak_linux_amd64.tar.gz", buf.Bytes())
	e.put("checksums.txt", []byte(sum(buf.Bytes())+"  cirak_linux_amd64.tar.gz\n"))
	out, code := e.run("--from-dir", e.fixture)
	if code == 0 || e.exists("home/.local/bin/cirak") {
		t.Fatalf("an archive without the cirak binary was accepted (exit %d):\n%s", code, out)
	}
}

func TestInstallRejectsUnknownArgumentsAndTouchesNothingElse(t *testing.T) {
	e := newInstallEnv(t)
	e.release("v0.1.0", "linux", "amd64", false)
	for _, args := range [][]string{{"--bogus"}, {"--version"}, {"--from-dir"}, {"--version", "0.1.0"}, {"--version", "v0.1.0; rm -rf /"}} {
		if out, code := e.run(args...); code == 0 {
			t.Errorf("%v was accepted:\n%s", args, out)
		}
	}
	if got := e.tree(); strings.Join(got, ",") != "home,tmp" {
		t.Errorf("a refused run wrote something: %v", got)
	}
	if out, code := e.run("--help"); code != 0 || !strings.Contains(out, "usage:") {
		t.Errorf("--help: exit %d:\n%s", code, out)
	}
}

// "Nothing is executed before verification" (ADR-026 section 4.1): an archive whose binary would leave a marker
// when run, with a wrong checksum, must be refused without the marker appearing.
func TestInstallRunsNothingBeforeVerification(t *testing.T) {
	e := newInstallEnv(t)
	marker := filepath.Join(e.root, "EXECUTED")
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	body := "#!/bin/sh\ntouch " + marker + "\necho 'cirak v9.9.9'\n"
	_ = tw.WriteHeader(&tar.Header{Name: "cirak", Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg})
	_, _ = tw.Write([]byte(body))
	_ = tw.Close()
	_ = gz.Close()
	e.put("cirak_linux_amd64.tar.gz", buf.Bytes())
	e.put("checksums.txt", []byte(strings.Repeat("b", 64)+"  cirak_linux_amd64.tar.gz\n"))
	out, code := e.run("--from-dir", e.fixture)
	if code == 0 {
		t.Fatalf("a wrong checksum was accepted:\n%s", out)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("the downloaded binary was executed before its checksum was verified")
	}
	// the same with a cosign bundle that fails: still nothing runs
	e2 := newInstallEnv(t)
	marker2 := filepath.Join(e2.root, "EXECUTED")
	var buf2 bytes.Buffer
	gz2 := gzip.NewWriter(&buf2)
	tw2 := tar.NewWriter(gz2)
	body2 := "#!/bin/sh\ntouch " + marker2 + "\necho 'cirak v9.9.9'\n"
	_ = tw2.WriteHeader(&tar.Header{Name: "cirak", Mode: 0o755, Size: int64(len(body2)), Typeflag: tar.TypeReg})
	_, _ = tw2.Write([]byte(body2))
	_ = tw2.Close()
	_ = gz2.Close()
	e2.put("cirak_linux_amd64.tar.gz", buf2.Bytes())
	e2.put("checksums.txt", []byte(sum(buf2.Bytes())+"  cirak_linux_amd64.tar.gz\n"))
	e2.put("checksums.txt.sigstore.json", []byte("{}\n"))
	e2.fake("cosign", "exit 1")
	if out, code := e2.run("--from-dir", e2.fixture); code == 0 {
		t.Fatalf("a failing cosign verification was accepted:\n%s", out)
	}
	if _, err := os.Stat(marker2); err == nil {
		t.Error("the downloaded binary was executed before the cosign verification")
	}
}

// A symlink named cirak inside a (correctly checksummed) archive must not be followed into the install.
func TestInstallRefusesASymlinkInPlaceOfTheBinary(t *testing.T) {
	e := newInstallEnv(t)
	secret := filepath.Join(e.root, "secret.txt")
	if err := os.WriteFile(secret, []byte("not a binary\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	_ = tw.WriteHeader(&tar.Header{Name: "cirak", Linkname: secret, Typeflag: tar.TypeSymlink, Mode: 0o777})
	_ = tw.Close()
	_ = gz.Close()
	e.put("cirak_linux_amd64.tar.gz", buf.Bytes())
	e.put("checksums.txt", []byte(sum(buf.Bytes())+"  cirak_linux_amd64.tar.gz\n"))
	out, code := e.run("--from-dir", e.fixture)
	if code == 0 || e.exists("home/.local/bin/cirak") {
		t.Fatalf("a symlink in place of the binary was installed (exit %d):\n%s", code, out)
	}
	mustContain(t, out, "not a regular file")
}

// The script is POSIX sh with no eval and never touches profiles, units, drop-ins or configuration.
func TestInstallScriptSourceRules(t *testing.T) {
	src := read(t, "install.sh")
	if !strings.HasPrefix(src, "#!/bin/sh\n") {
		t.Error("install.sh must start with #!/bin/sh")
	}
	if !strings.Contains(src, "set -eu") {
		t.Error("install.sh must set -eu")
	}
	for _, forbidden := range []string{"eval ", "eval\t", "bash", "[[", "source ", ".bashrc", ".profile", ".zshrc", "/etc/", "systemctl", "launchctl", "chown", "| sh", "|sh", "wget", "mktemp -u", "$RANDOM", "function "} {
		if strings.Contains(src, forbidden) {
			t.Errorf("install.sh contains %q", forbidden)
		}
	}
	if n := strings.Count(src, "sudo"); n == 0 {
		t.Error("install.sh must support --system through sudo")
	}
	if !strings.Contains(src, "--proto '=https'") || !strings.Contains(src, "--tlsv1.2") {
		t.Error("install.sh must download with --proto '=https' --tlsv1.2")
	}
}

// Syntax checks: sh -n always; dash -n and shellcheck when they exist. shellcheck is part of the CI runner;
// when it is not installed here the check is SKIPPED, and a skip is not a pass (the report lists it as UNVERIFIED).
func TestInstallScriptSyntaxAndShellcheck(t *testing.T) {
	script := filepath.Join(repoRoot(t), "install.sh")
	for _, shell := range []string{"sh", "dash"} {
		p, err := exec.LookPath(shell)
		if err != nil {
			continue
		}
		if out, err := exec.Command(p, "-n", script).CombinedOutput(); err != nil {
			t.Errorf("%s -n: %v\n%s", shell, err, out)
		}
	}
	sc, err := exec.LookPath("shellcheck")
	if err != nil {
		t.Skip("shellcheck is not installed here: UNVERIFIED locally (the CI runner has it)")
	}
	for _, f := range []string{"install.sh", "scripts/release/check-binary.sh", "scripts/release/check-archives.sh", "scripts/release/plain-build.sh", "scripts/release/reproduce.sh"} {
		if out, err := exec.Command(sc, "-s", "sh", filepath.Join(repoRoot(t), f)).CombinedOutput(); err != nil {
			t.Errorf("shellcheck %s: %v\n%s", f, err, out)
		}
	}
}
