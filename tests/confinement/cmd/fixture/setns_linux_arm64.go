//go:build linux && arm64

package main

// sysSetns is the setns(2) syscall number on linux/arm64. Source: Linux v7.3
// scripts/syscall.tbl (generic table used by arm64), "268 common setns
// sys_setns"; also include/uapi/asm-generic/unistd.h __NR_setns.
const sysSetns = 268
