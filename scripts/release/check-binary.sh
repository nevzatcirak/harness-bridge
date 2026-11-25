#!/bin/sh
# The per-artifact gate of ADR-026 section 3.1: `go version -m` must show a clean tree (vcs.modified=false), the
# expected revision, -trimpath=true and CGO_ENABLED=0. A failure stops the release.
#
# usage: check-binary.sh BINARY [REVISION]
set -eu

if [ "$#" -lt 1 ] || [ "$#" -gt 2 ]; then
	echo "usage: check-binary.sh BINARY [REVISION]" >&2
	exit 2
fi
bin=$1
rev=${2:-}
if [ ! -f "$bin" ]; then
	echo "check-binary: $bin is not a file" >&2
	exit 1
fi
info=$(go version -m "$bin") || {
	echo "check-binary: go version -m failed for $bin" >&2
	exit 1
}
fail=0
has() {
	printf '%s\n' "$info" | grep -Eq "^[[:space:]]+build[[:space:]]+$1\$"
}
if ! has 'vcs\.modified=false'; then
	echo "check-binary: $bin: vcs.modified is not false (the build tree was dirty or unstamped)" >&2
	fail=1
fi
if [ -n "$rev" ] && ! has "vcs\\.revision=$rev"; then
	echo "check-binary: $bin: vcs.revision is not $rev" >&2
	fail=1
fi
if ! has '-trimpath=true'; then
	echo "check-binary: $bin: was not built with -trimpath" >&2
	fail=1
fi
if ! has 'CGO_ENABLED=0'; then
	echo "check-binary: $bin: was not built with CGO_ENABLED=0" >&2
	fail=1
fi
if [ "$fail" -ne 0 ]; then
	exit 1
fi
echo "ok       $bin (vcs.modified=false${rev:+, revision $rev})"
