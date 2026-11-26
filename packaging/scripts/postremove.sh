#!/bin/sh
# nfpm postremove (deb postrm, rpm %postun): never start, stop or restart.
# A purge removes only /etc/cirak. State directories and the cirak user stay:
# the product never deletes persistent state, and the user may own it.
set -u

case "${1:-}" in
    purge)
        rm -rf /etc/cirak
        ;;
esac

exit 0
