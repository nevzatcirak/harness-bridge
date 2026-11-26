#!/bin/sh
# The post-release gates of ADR-026 section 3.1 on the archives GoReleaser produced:
#  - checksums.txt lists every archive with the right sha256;
#  - every archive holds the cirak binary, and that binary passes check-binary.sh (vcs.modified=false, the
#    expected revision);
#  - the binary equals the plain recipe's byte for byte (CIRAK_EQUALITY=arm64, the default: the linux/arm64
#    archive, as ADR-026 section 3.1 requires; CIRAK_EQUALITY=all: every archive; CIRAK_EQUALITY=none: skipped).
# Run it from the root of the checkout (the plain recipe builds there).
#
# usage: check-archives.sh DIST_DIR REVISION
set -eu

if [ "$#" -ne 2 ]; then
	echo "usage: check-archives.sh DIST_DIR REVISION" >&2
	exit 2
fi
dist=$1
rev=$2
mode=${CIRAK_EQUALITY:-arm64}
here=$(dirname "$0")

sha256() {
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum "$1" | cut -d' ' -f1
	elif command -v shasum >/dev/null 2>&1; then
		shasum -a 256 "$1" | cut -d' ' -f1
	else
		echo "check-archives: no sha256 tool" >&2
		exit 1
	fi
}

if [ ! -f "$dist/checksums.txt" ]; then
	echo "check-archives: $dist/checksums.txt is missing" >&2
	exit 1
fi
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
fail=0
count=0
for archive in "$dist"/cirak_*_*.tar.gz; do
	[ -f "$archive" ] || continue
	count=$((count + 1))
	name=$(basename "$archive")
	want=$(awk -v n="$name" '$2 == n { print $1 }' "$dist/checksums.txt")
	if [ -z "$want" ]; then
		echo "check-archives: checksums.txt does not list $name" >&2
		fail=1
		continue
	fi
	got=$(sha256 "$archive")
	if [ "$got" != "$want" ]; then
		echo "check-archives: $name: checksum mismatch (checksums.txt $want, file $got)" >&2
		fail=1
		continue
	fi
	dir="$work/$name"
	mkdir -p "$dir"
	if ! tar -xzf "$archive" -C "$dir" cirak 2>/dev/null; then
		echo "check-archives: $name has no cirak binary" >&2
		fail=1
		continue
	fi
	if ! "$here/check-binary.sh" "$dir/cirak" "$rev"; then
		fail=1
		continue
	fi
	plat=${name#cirak_}
	plat=${plat%.tar.gz}
	goos=${plat%%_*}
	goarch=${plat#*_}
	if [ "$mode" = all ] || { [ "$mode" = arm64 ] && [ "$plat" = linux_arm64 ]; }; then
		"$here/plain-build.sh" "$goos" "$goarch" "$dir/plain"
		if ! cmp -s "$dir/cirak" "$dir/plain"; then
			echo "check-archives: $name: the binary differs from the plain recipe's ($(sha256 "$dir/cirak") against $(sha256 "$dir/plain"))" >&2
			fail=1
			continue
		fi
		echo "ok       $name equals the plain recipe"
	else
		echo "ok       $name"
	fi
done
if [ "$count" -eq 0 ]; then
	echo "check-archives: no cirak_*_*.tar.gz in $dist" >&2
	exit 1
fi
if [ "$fail" -ne 0 ]; then
	exit 1
fi
