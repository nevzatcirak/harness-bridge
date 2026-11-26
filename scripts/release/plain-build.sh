#!/bin/sh
# The plain build recipe of ADR-026 section 3.1 (the H4/H5 recipe), without GoReleaser:
#   CGO_ENABLED=0 go build -trimpath -buildvcs=true -ldflags=-buildid=
# It is the reference the GoReleaser binary must equal byte for byte, and the build of the independent
# reproducibility job. Run it from the root of a clean checkout.
#
# usage: plain-build.sh GOOS GOARCH OUTPUT
# CIRAK_MAIN overrides the main package (default ./cmd/cirak); the tests use it.
set -eu

if [ "$#" -ne 3 ]; then
	echo "usage: plain-build.sh GOOS GOARCH OUTPUT" >&2
	exit 2
fi
main=${CIRAK_MAIN:-./cmd/cirak}
CGO_ENABLED=0 GOOS=$1 GOARCH=$2 go build -trimpath -buildvcs=true -ldflags=-buildid= -o "$3" "$main"
