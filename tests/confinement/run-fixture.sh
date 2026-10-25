#!/bin/sh
# Synthetic only. No service, credential, native API or production socket access.
set -eu
[ "$(id -u)" = 0 ] || { echo 'BLOCKED: root/CAP_SYS_ADMIN required for private mount/PID/network namespaces' >&2; exit 2; }
cd "$(dirname "$0")"
build_dir=$(mktemp -d /tmp/hb-confinement-build.XXXXXX)
trap 'rm -f "$build_dir/fixture"; rmdir "$build_dir"' EXIT HUP INT TERM
chmod 0755 "$build_dir"
CGO_ENABLED=0 go build -trimpath -o "$build_dir/fixture" ./cmd/fixture
# Outer namespaces make the baseline sockets and test processes synthetic too.
exec_env=/usr/bin/env
"$exec_env" -i PATH=/usr/bin:/bin /usr/bin/unshare --mount --net --ipc --pid --fork --kill-child --mount-proc "$build_dir/fixture" run "$(readlink /proc/self/ns/net)" "$(readlink /proc/self/ns/mnt)"
