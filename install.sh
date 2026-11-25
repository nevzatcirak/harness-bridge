#!/bin/sh
# install.sh - install the cirak binary from a GitHub release (ADR-026 section 4.1).
#
#   curl -fsSL https://github.com/nevzatcirak/cirak/releases/latest/download/install.sh -o install.sh
#   sh install.sh [--version vX.Y.Z] [--system] [--dry-run] [--from-dir DIR]
#
# POSIX sh, no eval. It downloads only over HTTPS from the release layout, verifies the sha256 of the archive
# against checksums.txt (always) and the cosign bundle of checksums.txt (when cosign is on the PATH), and runs
# nothing before that verification. It installs one file: $HOME/.local/bin/cirak by default, or
# /usr/local/bin/cirak with --system (sudo is used only for that copy). It never edits a shell profile, a unit,
# a drop-in or a configuration file, and it starts no service.
set -eu

# The release layout: $base/download/<tag>/<file> and $base/latest/download/<file>.
default_base='https://github.com/nevzatcirak/cirak/releases'
identity_re='^https://github\.com/nevzatcirak/cirak/\.github/workflows/release\.yml@refs/tags/v'
oidc_issuer='https://token.actions.githubusercontent.com'

usage() {
	cat <<'USAGE'
usage: install.sh [--version vX.Y.Z] [--system] [--dry-run] [--from-dir DIR]

  --version vX.Y.Z  install this release (default: the latest)
  --system          install to /usr/local/bin (uses sudo for that copy only)
  --dry-run         print the plan; download, write and run nothing
  --from-dir DIR    install from DIR (the archive, checksums.txt and optionally the cosign bundle);
                    no request is made
  -h, --help        this text

Environment:
  CIRAK_DOWNLOAD_BASE  an https:// mirror of the release layout (default: the GitHub releases of the project)
  CIRAK_VERIFY         "require" makes a missing cosign, or a missing cosign bundle, an error

Supported platforms: linux/amd64, linux/arm64, darwin/amd64, darwin/arm64.
USAGE
}

die() {
	printf 'install.sh: %s\n' "$*" >&2
	exit 1
}

version=''
from_dir=''
system=0
dry=0
while [ "$#" -gt 0 ]; do
	case "$1" in
	--version)
		[ "$#" -ge 2 ] || die '--version needs a value (for example v0.1.0)'
		version=$2
		shift 2
		;;
	--from-dir)
		[ "$#" -ge 2 ] || die '--from-dir needs a directory'
		from_dir=$2
		shift 2
		;;
	--system)
		system=1
		shift
		;;
	--dry-run)
		dry=1
		shift
		;;
	-h | --help)
		usage
		exit 0
		;;
	*)
		die "unknown argument: $1 (see --help)"
		;;
	esac
done

if [ -n "$version" ]; then
	case "$version" in
	v[0-9]*) ;;
	*) die "--version must look like v0.1.0, got: $version" ;;
	esac
	case "$version" in
	*[!0-9A-Za-z.+v-]*) die "--version contains a character that is not allowed: $version" ;;
	esac
fi

base=${CIRAK_DOWNLOAD_BASE:-$default_base}
case "$base" in
https://*) ;;
*) die "CIRAK_DOWNLOAD_BASE must start with https:// (got a different scheme or none)" ;;
esac
base=${base%/}

verify=${CIRAK_VERIFY:-auto}
case "$verify" in
auto | require) ;;
*) die "CIRAK_VERIFY must be auto or require" ;;
esac

# Platform. Anything else is refused with the list of supported ones.
os=$(uname -s)
arch=$(uname -m)
unsupported() {
	die "unsupported platform $os/$arch; supported: linux/amd64, linux/arm64, darwin/amd64, darwin/arm64"
}
case "$os" in
Linux) goos=linux ;;
Darwin) goos=darwin ;;
*) unsupported ;;
esac
case "$arch" in
x86_64 | amd64) goarch=amd64 ;;
aarch64 | arm64) goarch=arm64 ;;
*) unsupported ;;
esac

archive="cirak_${goos}_${goarch}.tar.gz"
checksums='checksums.txt'
bundle='checksums.txt.sigstore.json'

if [ -n "$version" ]; then
	url_base="$base/download/$version"
else
	url_base="$base/latest/download"
fi

if [ "$system" -eq 1 ]; then
	dest_dir=/usr/local/bin
else
	[ -n "${HOME:-}" ] || die 'HOME is not set; set it or use --system'
	dest_dir="$HOME/.local/bin"
fi
dest="$dest_dir/cirak"

if [ "$dry" -eq 1 ]; then
	echo 'plan:'
	echo "  platform    $goos/$goarch"
	if [ -n "$from_dir" ]; then
		echo "  from        $from_dir (no request is made)"
	else
		echo "  from        $url_base/$archive (https only)"
	fi
	echo "  verify      sha256 of the archive against $checksums; cosign bundle $bundle when cosign is present (CIRAK_VERIFY=$verify)"
	if [ "$system" -eq 1 ]; then
		echo "  install to  $dest (one sudo copy)"
	else
		echo "  install to  $dest"
	fi
	echo '  nothing was downloaded, written or run'
	exit 0
fi

sha256() {
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum "$1" | awk '{ print $1 }'
	elif command -v shasum >/dev/null 2>&1; then
		shasum -a 256 "$1" | awk '{ print $1 }'
	else
		die 'no sha256 tool: install sha256sum or shasum'
	fi
}

stage_file=''
tmp=''
cleanup() {
	if [ -n "$stage_file" ]; then
		rm -f "$stage_file"
	fi
	if [ -n "$tmp" ]; then
		rm -rf "$tmp"
	fi
}
trap cleanup EXIT
trap 'exit 1' INT TERM HUP

if [ -n "$from_dir" ]; then
	[ -d "$from_dir" ] || die "--from-dir: $from_dir is not a directory"
else
	command -v curl >/dev/null 2>&1 || die 'curl is required to download (or use --from-dir)'
fi

tmp=$(mktemp -d "${TMPDIR:-/tmp}/cirak-install.XXXXXX") || die 'cannot create a temporary directory'
dl="$tmp/dl"
mkdir "$dl"

fetch() {
	if [ -n "$from_dir" ]; then
		[ -f "$from_dir/$1" ] || return 1
		cp "$from_dir/$1" "$dl/$1"
	else
		curl --proto '=https' --tlsv1.2 -fsSL -o "$dl/$1" "$url_base/$1"
	fi
}

fetch "$archive" || die "cannot get $archive"
fetch "$checksums" || die "cannot get $checksums"
have_bundle=1
fetch "$bundle" || have_bundle=0

# 1. sha256, always.
want=$(awk -v n="$archive" '$2 == n { print $1; exit }' "$dl/$checksums")
[ -n "$want" ] || die "$checksums does not list $archive"
got=$(sha256 "$dl/$archive")
[ "$got" = "$want" ] || die "checksum mismatch for $archive (checksums.txt says $want, the file is $got)"

# 2. cosign, when it is available (or required).
if command -v cosign >/dev/null 2>&1; then
	if [ "$have_bundle" -eq 1 ]; then
		cosign verify-blob --bundle "$dl/$bundle" \
			--certificate-identity-regexp "$identity_re" \
			--certificate-oidc-issuer "$oidc_issuer" \
			"$dl/$checksums" >/dev/null 2>&1 || die "cosign verification failed for $checksums"
	elif [ "$verify" = require ]; then
		die "CIRAK_VERIFY=require: the release has no cosign bundle ($bundle)"
	else
		echo "install.sh: note: no cosign bundle for this release; verified by sha256 only" >&2
	fi
elif [ "$verify" = require ]; then
	die 'CIRAK_VERIFY=require: cosign is not on the PATH'
fi

# 3. Only now is anything unpacked or run.
mkdir "$tmp/x"
tar -xzf "$dl/$archive" -C "$tmp/x" cirak 2>/dev/null || die "$archive has no cirak binary"
if [ -L "$tmp/x/cirak" ] || [ ! -f "$tmp/x/cirak" ]; then
	die "$archive: the cirak entry is not a regular file"
fi
chmod 0755 "$tmp/x/cirak"
new=$("$tmp/x/cirak" version 2>/dev/null | head -n 1) || new=''
[ -n "$new" ] || die 'the downloaded cirak does not report its version'

old=''
if [ -x "$dest" ]; then
	old=$("$dest" version 2>/dev/null | head -n 1) || old=''
fi
if [ -n "$old" ] && [ "$old" = "$new" ]; then
	echo "$new is already installed at $dest"
	exit 0
fi

if [ "$system" -eq 1 ]; then
	if [ "$(id -u)" = 0 ]; then
		install -m 0755 "$tmp/x/cirak" "$dest"
	else
		command -v sudo >/dev/null 2>&1 || die 'sudo is required for --system'
		sudo install -m 0755 "$tmp/x/cirak" "$dest"
	fi
else
	mkdir -p "$dest_dir"
	stage_file="$dest_dir/.cirak.new.$$"
	cp "$tmp/x/cirak" "$stage_file"
	chmod 0755 "$stage_file"
	mv -f "$stage_file" "$dest"
	stage_file=''
fi

if [ -n "$old" ]; then
	echo "$new installed to $dest (replaced ${old#cirak })"
else
	echo "$new installed to $dest"
fi
if [ "$system" -eq 0 ]; then
	case ":${PATH:-}:" in
	*":$dest_dir:"*) ;;
	*) echo "install.sh: note: $dest_dir is not on your PATH; add it to run cirak" >&2 ;;
	esac
fi
