#!/bin/sh
# nfpm postinstall: reload systemd and print the next steps.
# It never enables, starts or restarts the service: without a token and an ACL
# the daemon cannot run, and a restart needs the ADR-010 idle fence.
set -eu

if command -v systemctl >/dev/null 2>&1; then
    systemctl daemon-reload || true
fi

cat <<'EOF'
cirak: installed but not enabled or started.

Next steps:
  1. install the token as /etc/cirak/token (mode 0600, readable by the cirak user)
     and write the host's settings in /etc/cirak/cirak.env, starting from
     /etc/cirak/cirak.env.example;
  2. systemctl enable --now cirak.service   (only when the configuration is ready);
  3. after an upgrade, restart when idle: a restart drops the process-local
     idempotency records and guard state (ADR-010), so never restart while a
     submission is unresolved.
EOF

exit 0
