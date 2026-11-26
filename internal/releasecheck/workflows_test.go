package releasecheck

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// repoRoot is the directory that holds go.mod, found from the test's working directory.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found above the test directory")
		}
		dir = parent
	}
}

// read returns a repository file, failing the test when it is missing (a missing file is RED, never a skip).
func read(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(repoRoot(t), rel))
	if err != nil {
		t.Fatalf("%s: %v", rel, err)
	}
	return string(b)
}

func workflowFiles(t *testing.T) []string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(repoRoot(t), ".github", "workflows", "*.yml"))
	if err != nil || len(matches) == 0 {
		t.Fatalf("no workflows found: %v", err)
	}
	sort.Strings(matches)
	return matches
}

// topBlock returns the lines of the top-level key (for example "on:" or "permissions:") of a workflow: the
// indented lines that follow it, up to the next line that starts in column 0.
func topBlock(src, key string) []string {
	var out []string
	in := false
	for _, line := range strings.Split(src, "\n") {
		if strings.HasPrefix(line, key) && !strings.HasPrefix(line, " ") {
			in = true
			continue
		}
		if in {
			if line != "" && !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "#") {
				break
			}
			out = append(out, line)
		}
	}
	return out
}

func compact(lines []string) string {
	var b strings.Builder
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		b.WriteString(l)
		b.WriteString("\n")
	}
	return b.String()
}

// ADR-026 section 3.1: the release workflow runs on a v* tag push and on nothing else.
func TestReleaseWorkflowRunsOnlyOnAVersionTagPush(t *testing.T) {
	src := read(t, ".github/workflows/release.yml")
	on := compact(topBlock(src, "on:"))
	if on == "" {
		t.Fatal("release.yml has no top-level on: block")
	}
	for _, forbidden := range []string{"pull_request", "pull_request_target", "workflow_dispatch", "schedule", "branches", "workflow_run", "release:", "repository_dispatch", "workflow_call"} {
		if strings.Contains(on, forbidden) {
			t.Errorf("release.yml triggers on %q; only a v* tag push may start a release:\n%s", forbidden, on)
		}
	}
	if on != "push:\ntags: ['v*']\n" && on != "push:\ntags:\n- 'v*'\n" {
		t.Errorf("release.yml must be exactly `on: push: tags: ['v*']`, got:\n%s", on)
	}
}

// The snapshot workflow checks the release configuration on every pull request and does nothing else.
func TestSnapshotWorkflowRunsOnPullRequestsAndNeverSignsOrPublishes(t *testing.T) {
	src := read(t, ".github/workflows/snapshot.yml")
	on := compact(topBlock(src, "on:"))
	if !strings.Contains(on, "pull_request:") {
		t.Errorf("snapshot.yml must run on pull_request:\n%s", on)
	}
	for _, forbidden := range []string{"pull_request_target", "tags", "workflow_dispatch", "schedule", "release:"} {
		if strings.Contains(on, forbidden) {
			t.Errorf("snapshot.yml triggers on %q", forbidden)
		}
	}
	for _, want := range []string{"goreleaser", "--snapshot", "--single-target", "check"} {
		if !strings.Contains(src, want) {
			t.Errorf("snapshot.yml lacks %q", want)
		}
	}
	for _, forbidden := range []string{"cosign", "attest", "id-token", "sign", "gh release", "--draft", "contents: write", "attestations: write", "actions/upload-artifact", "release --"} {
		if strings.Contains(src, forbidden) {
			t.Errorf("snapshot.yml must build without signing or publishing, but contains %q", forbidden)
		}
	}
}

var pinnedUse = regexp.MustCompile(`^\s*-?\s*uses:\s+[A-Za-z0-9_.-]+/[A-Za-z0-9_./-]+@[0-9a-f]{40}\s+#\s*v\d+`)

// ADR-026 section 3.1: every action is pinned to a full commit SHA with a version comment.
func TestEveryActionIsPinnedToAFullCommitSHA(t *testing.T) {
	for _, f := range workflowFiles(t) {
		src, _ := os.ReadFile(f)
		n := 0
		for i, line := range strings.Split(string(src), "\n") {
			if !strings.Contains(line, "uses:") || strings.HasPrefix(strings.TrimSpace(line), "#") {
				continue
			}
			n++
			if strings.Contains(line, "uses: ./") {
				continue
			}
			if !pinnedUse.MatchString(line) {
				t.Errorf("%s:%d is not pinned by a full SHA with a version comment: %s", filepath.Base(f), i+1, strings.TrimSpace(line))
			}
		}
		if n == 0 {
			t.Errorf("%s uses no action: the pin check would be vacuous", filepath.Base(f))
		}
	}
}

// Least privilege: the default token is read-only; write permissions exist only on the release workflow's
// build job (contents, id-token, attestations) and nowhere else.
func TestWorkflowPermissionsAreLeastPrivilege(t *testing.T) {
	for _, name := range []string{"release.yml", "snapshot.yml", "ci.yml"} {
		src := read(t, ".github/workflows/"+name)
		top := compact(topBlock(src, "permissions:"))
		if top != "contents: read\n" {
			t.Errorf("%s: the top-level permissions must be exactly `contents: read`, got %q", name, top)
		}
		if strings.Contains(src, "write-all") || strings.Contains(src, "read-all") {
			t.Errorf("%s uses a blanket permission", name)
		}
	}
	for _, name := range []string{"snapshot.yml", "ci.yml"} {
		if strings.Contains(read(t, ".github/workflows/"+name), ": write") {
			t.Errorf("%s must not request any write permission", name)
		}
	}
	rel := read(t, ".github/workflows/release.yml")
	for _, perm := range []string{"contents: write", "id-token: write", "attestations: write"} {
		if strings.Count(rel, perm) != 1 {
			t.Errorf("release.yml must request %q exactly once (on the build job), found %d", perm, strings.Count(rel, perm))
		}
	}
	for _, other := range regexp.MustCompile(`(?m)^\s+([a-z-]+): write`).FindAllStringSubmatch(rel, -1) {
		switch other[1] {
		case "contents", "id-token", "attestations":
		default:
			t.Errorf("release.yml requests an unexpected write permission: %s", other[1])
		}
	}
	// The write permissions belong to the build job, not to the reproducibility job.
	reproduce := jobBlock(rel, "reproduce")
	if strings.Contains(reproduce, ": write") {
		t.Errorf("the reproduce job must not hold a write permission:\n%s", reproduce)
	}
}

// jobBlock returns the text of one job under jobs: (from its key to the next job key).
func jobBlock(src, job string) string {
	lines := strings.Split(src, "\n")
	var out []string
	in := false
	for _, l := range lines {
		if regexp.MustCompile(`^  [a-z][a-z0-9_-]*:\s*$`).MatchString(l) {
			if strings.TrimSpace(l) == job+":" {
				in = true
				out = append(out, l)
				continue
			}
			if in {
				break
			}
		}
		if in {
			out = append(out, l)
		}
	}
	return strings.Join(out, "\n")
}

// The agent never creates secrets, environments, tags or releases (AGENTS.md): the workflows reference only the
// built-in token, define no environment, and run no command that tags, pushes or publishes.
func TestWorkflowsCreateNoSecretsEnvironmentsTagsOrPublications(t *testing.T) {
	secret := regexp.MustCompile(`secrets\.([A-Za-z0-9_]+)`)
	for _, f := range workflowFiles(t) {
		b, _ := os.ReadFile(f)
		src := string(b)
		for _, m := range secret.FindAllStringSubmatch(src, -1) {
			if m[1] != "GITHUB_TOKEN" {
				t.Errorf("%s references the secret %s; S2 may use only GITHUB_TOKEN", filepath.Base(f), m[1])
			}
		}
		if regexp.MustCompile(`(?m)^\s+environment:`).MatchString(src) {
			t.Errorf("%s defines an environment; the maintainer creates it (ADR-026 section 6)", filepath.Base(f))
		}
		for _, forbidden := range []string{"git tag", "git push", "gh release", "gh api -X", "gh api --method", "npm publish", "brew ", "HOMEBREW_TAP_TOKEN", "NPM_TOKEN"} {
			if strings.Contains(src, forbidden) {
				t.Errorf("%s contains %q", filepath.Base(f), forbidden)
			}
		}
	}
}

// ADR-026 section 3.2: the build job makes a draft and the maintainer publishes by hand; a second job rebuilds
// independently, on a different runner image, without GoReleaser.
func TestReleaseWorkflowHasABuildJobAndAnIndependentReproducibilityJob(t *testing.T) {
	src := read(t, ".github/workflows/release.yml")
	build, reproduce := jobBlock(src, "build"), jobBlock(src, "reproduce")
	if build == "" || reproduce == "" {
		t.Fatalf("release.yml needs a `build` and a `reproduce` job (build=%d reproduce=%d bytes)", len(build), len(reproduce))
	}
	runsOn := regexp.MustCompile(`runs-on:\s*(\S+)`)
	b, r := runsOn.FindStringSubmatch(build), runsOn.FindStringSubmatch(reproduce)
	if b == nil || r == nil || b[1] == r[1] {
		t.Errorf("the reproduce job must run on a different runner image than the build job: build=%v reproduce=%v", b, r)
	}
	if !strings.Contains(reproduce, "needs: build") && !strings.Contains(reproduce, "needs: [build]") {
		t.Errorf("the reproduce job must depend on the build job")
	}
	if strings.Contains(reproduce, "goreleaser") {
		t.Errorf("the independent rebuild must not use GoReleaser")
	}
	for _, want := range []string{"scripts/release/reproduce.sh"} {
		if !strings.Contains(reproduce, want) {
			t.Errorf("the reproduce job does not run %s", want)
		}
	}
	for _, want := range []string{"scripts/release/check-binary.sh", "scripts/release/check-archives.sh", "scripts/release/plain-build.sh", "goreleaser", "cosign", "actions/attest"} {
		if !strings.Contains(build, want) {
			t.Errorf("the build job lacks %s", want)
		}
	}
	// The gates run before the draft is created, and again on the produced archives.
	iBuild := strings.Index(build, "goreleaser-action")
	iGate := strings.Index(build, "check-binary.sh")
	iRelease := strings.LastIndex(build, "goreleaser-action")
	if iBuild < 0 || iGate < 0 || iGate > iRelease {
		t.Errorf("the pre-release gate (check-binary.sh) must run between the GoReleaser build and the GoReleaser release steps")
	}
	if !strings.Contains(build, "tag") {
		t.Errorf("the build job must check out the tag with full history")
	}
}

// ADR-026 section 3.1: the toolchain is local and exact in every workflow that builds.
func TestBuildingWorkflowsPinTheToolchain(t *testing.T) {
	for _, name := range []string{"release.yml", "snapshot.yml", "ci.yml"} {
		src := read(t, ".github/workflows/"+name)
		for _, want := range []string{"GOTOOLCHAIN: local", "go-version: '1.27.2'", "go1.27.2"} {
			if !strings.Contains(src, want) {
				t.Errorf("%s lacks %q", name, want)
			}
		}
	}
}

func TestGitignoreHasDist(t *testing.T) {
	found := false
	for _, l := range strings.Split(read(t, ".gitignore"), "\n") {
		if strings.TrimSpace(l) == "dist/" {
			found = true
		}
	}
	if !found {
		t.Error(".gitignore must list dist/ (finding F-H5-5: GoReleaser writes ./dist into the clone)")
	}
}

// ADR-026 section 3.1 and 5.2: the recipe, the targets, the checksums, the SBOM, the signature, the draft.
func TestGoReleaserConfigFollowsTheADR(t *testing.T) {
	src := read(t, ".goreleaser.yaml")
	for _, want := range []string{
		"version: 2",
		"main: ./cmd/cirak",
		"- CGO_ENABLED=0",
		"- -trimpath",
		"- -buildvcs=true",
		"- -buildid=",
		"mod_timestamp: \"{{ .CommitTimestamp }}\"",
		"- linux",
		"- darwin",
		"- amd64",
		"- arm64",
		"draft: true",
		"name_template: checksums.txt",
		"algorithm: sha256",
		"spdx-json",
		"cosign",
		"sign-blob",
		"--bundle",
		"use: git",
		"^test:",
		"go1.27.2",
		"OpenCode 1.19.4",
		"cosign verify-blob",
		"gh attestation verify",
		"Harness Bridge is now Cirak",
	} {
		if !strings.Contains(src, want) {
			t.Errorf(".goreleaser.yaml lacks %q", want)
		}
	}
	if strings.Contains(src, "windows") {
		t.Error("Windows is not a target in v0.1.0 (ADR-026 section 3.1, Q5)")
	}
	if strings.Contains(src, "-s -w") || strings.Contains(src, "main.version") {
		t.Error("the default ldflags (-s -w -X main.version) change the bytes; the recipe sets -buildid= only")
	}
	// install.sh is a release asset and is listed in the checksums.
	if strings.Count(src, "- glob: ./install.sh") != 2 {
		t.Error("install.sh must be both a release extra file and a checksum extra file (two `- glob: ./install.sh` entries)")
	}
	// A pre-release gate and the draft publication gate: nothing publishes the release itself.
	if strings.Contains(src, "prerelease: false") || strings.Contains(src, "draft: false") {
		t.Error("the release must be a draft")
	}
}

// ADR-026 section 4.1 and the D33 amendment: the one ADR edit allowed in this slice.
func TestADRRecordsD33AsTheOnlyAmendment(t *testing.T) {
	src := read(t, "docs/adr/ADR-026-cirak-name-and-distribution.md")
	if !strings.Contains(src, "D33") || !strings.Contains(src, "@nevzatcirak/cirak") || !strings.Contains(src, "D29 Q8") {
		t.Error("ADR-026 must record D33 (npm package scoped @nevzatcirak/cirak, superseding D29 Q8)")
	}
	if n := strings.Count(src, "Amendment (D33"); n != 1 {
		t.Errorf("the D33 note must appear exactly once, found %d", n)
	}
}
