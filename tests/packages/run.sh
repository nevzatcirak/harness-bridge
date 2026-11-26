#!/bin/sh
# ADR-026 S4 package tests: build the deb and rpm packages from .goreleaser.yaml,
# then install them in a disposable Debian and Fedora container whose PID 1 is
# systemd. The host systemd is never touched and nothing is installed on the host.
#
# Requires: docker (with permission to run a privileged disposable container),
# goreleaser (set GORELEASER_BIN or put goreleaser on PATH), go.
set -eu

root=$(cd -- "$(dirname -- "$0")/../.." && pwd)
cd "$root"
tests="$root/tests/packages"
goreleaser=${GORELEASER_BIN:-goreleaser}

ok() { printf 'ok - %s\n' "$*"; }
fail() { printf 'not ok - %s\n' "$*" >&2; exit 1; }

command -v docker >/dev/null 2>&1 || fail "docker is required for the container tests"
docker info >/dev/null 2>&1 || fail "the docker daemon is not reachable"
command -v "$goreleaser" >/dev/null 2>&1 || fail "goreleaser is required (set GORELEASER_BIN)"

sh "$tests/scripts-test.sh"

sandbox=$(mktemp -d "${TMPDIR:-/tmp}/cirak-pkg-test.XXXXXX")
name_deb="cirak-pkg-deb-$$"
name_rpm="cirak-pkg-rpm-$$"
cleanup() {
    if [ -f "$sandbox/env.orig" ]; then
        cp "$sandbox/env.orig" packaging/cirak.env.example
    fi
    docker rm -f "$name_deb" "$name_rpm" >/dev/null 2>&1 || true
    rm -rf "$sandbox"
}
trap cleanup EXIT HUP INT TERM

build_version() {
    version=$1
    out=$2
    cfg="$sandbox/goreleaser-$version.yaml"
    sed "s|^  version_template: .*|  version_template: \"$version\"|" .goreleaser.yaml >"$cfg"
    grep -q "version_template: \"$version\"" "$cfg" || fail "could not set the snapshot version to $version"
    printf '== building packages at version %s\n' "$version"
    if ! "$goreleaser" release --snapshot --clean --skip=publish,sign,sbom -f "$cfg" >"$sandbox/build-$version.log" 2>&1; then
        cat "$sandbox/build-$version.log" >&2
        fail "goreleaser failed for version $version"
    fi
    mkdir -p "$out"
    cp dist/*.deb dist/*.rpm "$out"/ 2>/dev/null || {
        cat "$sandbox/build-$version.log" >&2
        fail "no deb/rpm packages were built for version $version"
    }
}

case "$(uname -m)" in
    x86_64|amd64) arch=amd64 ;;
    aarch64|arm64) arch=arm64 ;;
    *) fail "unsupported container architecture: $(uname -m)" ;;
esac

build_version 1.0.0 "$sandbox/v1"
# v2 ships a changed example environment file, so the upgrade test can prove the
# config|noreplace behaviour (the edited local file is kept; the new upstream one
# lands as .dpkg-dist / .rpmnew) instead of merely reinstalling identical content.
cp packaging/cirak.env.example "$sandbox/env.orig"
printf '\n# package-test-upstream-v2\n' >>packaging/cirak.env.example
build_version 1.0.1 "$sandbox/v2"
cp "$sandbox/env.orig" packaging/cirak.env.example

for kind in deb rpm; do
    [ -f "$sandbox/v1/cirak_1.0.0_linux_${arch}.${kind}" ] || fail "v1 $kind package is missing"
    [ -f "$sandbox/v2/cirak_1.0.1_linux_${arch}.${kind}" ] || fail "v2 $kind package is missing"
done
ok "built v1 and v2 deb/rpm packages for linux/$arch"

mkdir -p "$sandbox/pkgs"
for kind in deb rpm; do
    cp "$sandbox/v1/cirak_1.0.0_linux_${arch}.${kind}" "$sandbox/pkgs/"
    cp "$sandbox/v2/cirak_1.0.1_linux_${arch}.${kind}" "$sandbox/pkgs/"
done

printf '== building the disposable test images\n'
platform="linux/$arch"
docker pull --quiet --platform "$platform" debian:bookworm-slim >/dev/null || fail "could not pull debian:bookworm-slim for $platform"
docker pull --quiet --platform "$platform" fedora:41 >/dev/null || fail "could not pull fedora:41 for $platform"
docker build --platform "$platform" -q -t cirak-pkg-test-debian -f "$tests/images/debian.Dockerfile" "$tests/images" >/dev/null || fail "the Debian image build failed"
docker build --platform "$platform" -q -t cirak-pkg-test-fedora -f "$tests/images/fedora.Dockerfile" "$tests/images" >/dev/null || fail "the Fedora image build failed"

wait_systemd() {
    container=$1
    i=0
    while [ "$i" -lt 120 ]; do
        state=$(docker exec "$container" systemctl is-system-running 2>/dev/null || true)
        case "$state" in
            running|degraded) return 0 ;;
        esac
        i=$((i + 1))
        sleep 2
    done
    docker logs "$container" >&2 || true
    fail "systemd did not start in $container"
}

run_container() {
    kind=$1
    image=$2
    name=$3
    v1="cirak_1.0.0_linux_${arch}.${kind}"
    v2="cirak_1.0.1_linux_${arch}.${kind}"

    printf '== %s install test\n' "$kind"
    docker run -d --name "$name" --hostname "cirak-pkg-test" --platform "linux/$arch" --privileged --cgroupns=host \
        -v /sys/fs/cgroup:/sys/fs/cgroup:rw \
        -v "$sandbox/pkgs:/pkg:ro" \
        -v "$tests:/tests:ro" \
        "$image" /sbin/init >/dev/null || fail "docker run failed for $kind"
    wait_systemd "$name"
    if ! docker exec "$name" /bin/sh /tests/container-test.sh "$kind" "/pkg/$v1" "/pkg/$v2"; then
        docker logs "$name" >&2 || true
        fail "the $kind container test failed"
    fi
    docker rm -f "$name" >/dev/null 2>&1 || true
    ok "$kind: install, Restart=no, upgrade and removal checks passed"
}

run_container deb cirak-pkg-test-debian "$name_deb"
run_container rpm cirak-pkg-test-fedora "$name_rpm"

printf 'PASS: ADR-026 S4 package tests\n'
