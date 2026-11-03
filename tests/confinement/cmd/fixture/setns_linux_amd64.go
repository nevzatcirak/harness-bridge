//go:build linux && amd64

package main

// sysSetns is the setns(2) syscall number on linux/amd64. Package syscall does
// not define SYS_SETNS for amd64. Source: Linux v7.3
// arch/x86/entry/syscalls/syscall_64.tbl, "308 common setns sys_setns".
const sysSetns = 308
