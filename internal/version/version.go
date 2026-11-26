// Package version reports the version of the running binary. The value is the module version
// the Go toolchain stamps into a build made with -buildvcs=true (ADR-026 section 5.1): a tagged
// build reports the tag, an untagged one a pseudo-version, and no literal is kept in the source.
package version

import (
	"runtime/debug"
	"strings"
)

// Version is the module version of this binary, "devel" when the toolchain stamped none
// (a plain `go test` or `go run` binary) and "unknown" when no build information is embedded.
func Version() string {
	bi, ok := debug.ReadBuildInfo()
	return fromBuildInfo(bi, ok)
}

func fromBuildInfo(bi *debug.BuildInfo, ok bool) string {
	if !ok || bi == nil {
		return "unknown"
	}
	if v := bi.Main.Version; v != "" && v != "(devel)" {
		return v
	}
	return "devel"
}

// Detail is the text of `cirak version`: the version, the revision with its modified flag when
// the build stamped them, and the toolchain. It prints only what the build information holds.
func Detail(bi *debug.BuildInfo, ok bool) string {
	var b strings.Builder
	b.WriteString("cirak " + fromBuildInfo(bi, ok) + "\n")
	if !ok || bi == nil {
		return b.String()
	}
	var rev, modified string
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.modified":
			modified = s.Value
		}
	}
	if rev != "" {
		b.WriteString("revision " + rev)
		if modified != "" {
			b.WriteString(" modified=" + modified)
		}
		b.WriteString("\n")
	}
	if bi.GoVersion != "" {
		b.WriteString("go " + bi.GoVersion + "\n")
	}
	return b.String()
}

// Text is Detail for the running binary.
func Text() string {
	bi, ok := debug.ReadBuildInfo()
	return Detail(bi, ok)
}

// Build returns the VCS revision the toolchain stamped into this binary and whether the tree was modified, or
// "" when the build carries no revision (a plain `go test` or `go run` binary).
func Build() (revision string, modified bool) {
	bi, ok := debug.ReadBuildInfo()
	if !ok || bi == nil {
		return "", false
	}
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			revision = s.Value
		case "vcs.modified":
			modified = s.Value == "true"
		}
	}
	return revision, modified
}
