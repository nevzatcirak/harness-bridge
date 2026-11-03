//go:build linux && !amd64 && !arm64

package main

// The seccomp filter needs a verified setns(2) syscall number per
// architecture. Add a reviewed setns_linux_<arch>.go before building here.
const sysSetns = confinementFixtureSupportsOnlyLinuxAmd64AndArm64_AddVerifiedSetnsSyscallNumber
