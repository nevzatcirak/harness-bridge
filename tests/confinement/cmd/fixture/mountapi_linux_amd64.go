//go:build linux && amd64

package main

// New mount API syscall numbers on linux/amd64. Package syscall defines none of
// them. Source: Linux v7.3 arch/x86/entry/syscalls/syscall_64.tbl:
// "428 common open_tree", "429 common move_mount", "430 common fsopen",
// "431 common fsconfig", "432 common fsmount", "433 common fspick",
// "442 common mount_setattr", "467 common open_tree_attr".
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
