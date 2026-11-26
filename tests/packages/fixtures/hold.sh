#!/bin/sh
# A long-running stub that stands in for the daemon during the upgrade test.
# The package upgrade must not stop it and must not change its main PID.
trap 'exit 0' TERM INT
while :; do
    sleep 1
done
