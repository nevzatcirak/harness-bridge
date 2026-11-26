#!/bin/sh
# nfpm preinstall: create the cirak system user and group idempotently.
# No login, no home, no password. The package does not touch any existing user.
set -eu

if ! getent group cirak >/dev/null 2>&1; then
    groupadd --system cirak
fi

if ! getent passwd cirak >/dev/null 2>&1; then
    nologin=$(command -v nologin || true)
    if [ -z "$nologin" ]; then
        nologin=/bin/false
    fi
    if command -v useradd >/dev/null 2>&1; then
        useradd --system --gid cirak --no-create-home --home-dir /nonexistent --shell "$nologin" cirak
    else
        adduser --system --ingroup cirak --no-create-home --home /nonexistent --shell "$nologin" --disabled-login cirak
    fi
fi

exit 0
