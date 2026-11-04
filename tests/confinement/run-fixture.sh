#!/bin/sh
# Synthetic only. No service, credential, native API or production socket access.
# DISPOSABLE TEST HOST ONLY: never run this on a host that runs the Bridge or
# OpenCode. The fixture runs as root and writes under the host's /tmp.
set -eu

# Fail closed unless the operator explicitly acknowledges a disposable host
# (R2-PR5-FIXTURE-REVIEW F-B). Never set this in CI or in a shell profile.
ack_value=disposable-host-without-bridge-or-opencode
if [ "${HB_CONFINEMENT_DISPOSABLE_HOST:-}" != "$ack_value" ]; then
	echo "BLOCKED: disposable host only. Set HB_CONFINEMENT_DISPOSABLE_HOST to $ack_value only on a disposable test host that runs no Bridge or OpenCode." >&2
	exit 2
fi
# Accident guard, not proof of disposability: refuse hosts that have the
# Bridge or OpenCode installed or running.
for marker in /etc/harness-bridge /var/lib/harness-bridge /etc/systemd/system/harness-bridge* /etc/systemd/system/opencode* /usr/lib/systemd/system/harness-bridge* /usr/lib/systemd/system/opencode* /run/systemd/system/harness-bridge* /run/systemd/system/opencode*; do
	if [ -e "$marker" ] || [ -L "$marker" ]; then
		echo "BLOCKED: Bridge/OpenCode host marker present: $marker" >&2
		exit 2
	fi
done
for comm in /proc/[0-9]*/comm; do
	name=$(cat "$comm" 2>/dev/null) || continue
	case "$name" in
	harness-bridge*|opencode*)
		echo "BLOCKED: Bridge/OpenCode process running: $name" >&2
		exit 2
		;;
	esac
done
[ "$(id -u)" = 0 ] || { echo 'BLOCKED: root/CAP_SYS_ADMIN required for private mount/PID/network namespaces' >&2; exit 2; }
cd "$(dirname "$0")"
go_bin=$(command -v go) || { echo 'BLOCKED: go toolchain not found' >&2; exit 2; }

# Cleanup runs on every shell exit path, including failures and INT/TERM/HUP
# (F-C). It is idempotent, and rm -rf never follows symlinks out of the tree.
# Only SIGKILL of this shell can skip it.
build_dir=
work_dir=
cleanup() {
	if [ -n "$work_dir" ]; then rm -rf -- "$work_dir"; fi
	if [ -n "$build_dir" ]; then rm -rf -- "$build_dir"; fi
}
trap 'status=$?; cleanup; exit $status' EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM
build_dir=$(mktemp -d /tmp/hb-confinement-build.XXXXXX)
work_dir=$(mktemp -d /tmp/hb-confinement-fixture-XXXXXXXXXX)
chmod 0755 "$build_dir" "$work_dir"

# Build with a cleared, fixed Go environment (F-F): no inherited GOFLAGS,
# toolexec, go env file, workspace or toolchain switch, no network, no git
# invocation as root (-buildvcs=false), and a build cache inside the cleaned
# build directory.
build_env='GOENV=off GOTOOLCHAIN=local GOFLAGS=-mod=readonly GOPROXY=off GOSUMDB=off GOWORK=off CGO_ENABLED=0'
# shellcheck disable=SC2086 # build_env is a fixed list of assignments
/usr/bin/env -i PATH=/usr/bin:/bin HOME="$build_dir/home" GOCACHE="$build_dir/gocache" GOPATH="$build_dir/gopath" $build_env "$go_bin" build -trimpath -buildvcs=false -o "$build_dir/fixture" ./cmd/fixture

# Outer namespaces make the baseline sockets and test processes synthetic too.
/usr/bin/env -i PATH=/usr/bin:/bin HB_CONFINEMENT_DISPOSABLE_HOST="$HB_CONFINEMENT_DISPOSABLE_HOST" /usr/bin/unshare --mount --net --ipc --pid --fork --kill-child --mount-proc "$build_dir/fixture" run "$(readlink /proc/self/ns/net)" "$(readlink /proc/self/ns/mnt)" "$work_dir"
