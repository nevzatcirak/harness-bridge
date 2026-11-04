//go:build linux && !amd64 && !arm64

package main

// The seccomp filter needs verified new mount API syscall numbers per
// architecture. Add a reviewed mountapi_linux_<arch>.go before building here.
const sysOpenTree = confinementFixtureSupportsOnlyLinuxAmd64AndArm64_AddVerifiedMountAPISyscallNumbers
