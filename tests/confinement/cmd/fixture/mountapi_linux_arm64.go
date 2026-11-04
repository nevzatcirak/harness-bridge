//go:build linux && arm64

package main

// New mount API syscall numbers on linux/arm64. Package syscall defines none of
// them. Source: Linux v7.3 scripts/syscall.tbl (generic table used by arm64 via
// arch/arm64/tools/syscall_64.tbl, byte-identical in v7.3) and
// include/uapi/asm-generic/unistd.h: __NR_open_tree 428, __NR_move_mount 429,
// __NR_fsopen 430, __NR_fsconfig 431, __NR_fsmount 432, __NR_fspick 433,
// __NR_mount_setattr 442, __NR_open_tree_attr 467.
const (
	sysOpenTree     = 428
	sysMoveMount    = 429
	sysFsopen       = 430
	sysFsconfig     = 431
	sysFsmount      = 432
	sysFspick       = 433
	sysMountSetattr = 442
	sysOpenTreeAttr = 467
)
