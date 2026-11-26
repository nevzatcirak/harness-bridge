#!/bin/sh
# nfpm preremove (deb prerm, rpm %preun): stop the unit only on a removal.
# deb passes "remove" on a removal and "upgrade"/"failed-upgrade"/"deconfigure"
# on an upgrade; rpm passes 0 on a removal and 1 (or more) on an upgrade.
# An upgrade must never stop the unit: a stop drops the process-local
# idempotency records and guard state (ADR-010), exactly like a restart.
set -u

case "${1:-}" in
    remove|0)
        if command -v systemctl >/dev/null 2>&1; then
            systemctl stop cirak.service || true
        fi
        ;;
esac

exit 0
