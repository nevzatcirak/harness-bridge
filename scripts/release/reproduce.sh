#!/bin/sh
# The independent rebuild of ADR-026 section 3.2 (2): for every archive in DIST_DIR, rebuild the same commit
# with the plain recipe (no GoReleaser) and compare the sha256 of the binary with the one in the archive. One
# byte of difference fails the job and leaves the release a draft. Run it from the root of a checkout of the
# tagged commit, on a runner image other than the one that built the archives.
#
# usage: reproduce.sh DIST_DIR
set -eu

if [ "$#" -ne 1 ]; then
	echo "usage: reproduce.sh DIST_DIR" >&2
	exit 2
fi
dist=$1
here=$(dirname "$0")
if [ ! -d "$dist" ]; then
	echo "reproduce: $dist is not a directory" >&2
	exit 1
fi

sha256() {
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum "$1" | cut -d' ' -f1
	elif command -v shasum >/dev/null 2>&1; then
		shasum -a 256 "$1" | cut -d' ' -f1
	else
		echo "reproduce: no sha256 tool" >&2
		exit 1
	fi
}

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
fail=0
count=0
for archive in "$dist"/cirak_*_*.tar.gz; do
	[ -f "$archive" ] || continue
	count=$((count + 1))
	name=$(basename "$archive")
	plat=${name#cirak_}
	plat=${plat%.tar.gz}
	goos=${plat%%_*}
	goarch=${plat#*_}
	dir="$work/$plat"
	mkdir -p "$dir"
	if ! tar -xzf "$archive" -C "$dir" cirak 2>/dev/null; then
		echo "MISMATCH $plat: the archive has no cirak binary" >&2
		fail=1
		continue
	fi
	"$here/plain-build.sh" "$goos" "$goarch" "$dir/rebuilt"
	from=$(sha256 "$dir/cirak")
	to=$(sha256 "$dir/rebuilt")
	if [ "$from" = "$to" ]; then
		echo "ok       $plat $to"
	else
		echo "MISMATCH $plat archive=$from rebuilt=$to" >&2
		fail=1
	fi
done
if [ "$count" -eq 0 ]; then
	echo "reproduce: no cirak_*_*.tar.gz in $dist" >&2
	exit 1
fi
if [ "$fail" -ne 0 ]; then
	echo "reproduce: NOT reproducible: the release stays a draft" >&2
	exit 1
fi
echo "reproducible: $count archive(s) match the independent rebuild"
