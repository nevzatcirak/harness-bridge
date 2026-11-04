# Evidence runbook rules

## Recording a running binary via `/proc/<pid>/exe`

Record `/proc/<pid>/exe` (path and SHA-256) only after `ls -l /proc/<pid>/exe`
shows the expected target binary.

Right after a service start or restart, systemd 255 can still show
`systemd-executor` as the target, not the service binary. A hash taken at that
moment identifies systemd, not the Bridge. Wait, re-run `ls -l`, and hash only
once the link points at the service binary.

Known `systemd-executor` hash (do not record it as a service binary):

| systemd package | arch | path | SHA-256 |
| --- | --- | --- | --- |
| 255.4-1ubuntu8.18 | aarch64 | `/usr/lib/systemd/systemd-executor` | `8390bf374743494c8a8905af656dbaccd55992f49e9386884a241349ac6d6400` |

The full hash was computed on 2026-11-04 on an Ubuntu systemd 255.4-1ubuntu8.18
aarch64 host and matches the prefix `8390bf37…` recorded by the Planning Desk.
Other systemd builds have different hashes. Always check the `ls -l` target,
not only the hash.
