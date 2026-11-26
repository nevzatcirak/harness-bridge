#!/bin/sh
# Fast checks of the S4 packaging files, without a container or a root action.
# They catch the three RED mutations of ADR-026 section 8/9:
#   - the unit with Restart=on-failure;
#   - a preremove that stops unconditionally (an upgrade then stops the unit);
#   - a postinstall that starts, enables or restarts the unit.
set -eu

root=$(cd -- "$(dirname -- "$0")/../.." && pwd)
pkg="$root/packaging"
gr="$root/.goreleaser.yaml"

ok() { printf 'ok - %s\n' "$*"; }
fail() { printf 'not ok - %s\n' "$*" >&2; exit 1; }

# ---- static files -----------------------------------------------------------------

[ -f "$pkg/cirak.service" ] || fail "packaging/cirak.service is missing"
grep -qx 'Restart=no' "$pkg/cirak.service" || fail "the unit is not Restart=no"
count=$(grep -c '^Restart=' "$pkg/cirak.service" || true)
[ "$count" = 1 ] || fail "the unit has $count Restart= lines, want exactly 1"
ok "the unit is Restart=no"

[ -f "$pkg/cirak.env.example" ] || fail "packaging/cirak.env.example is missing"
for s in preinstall postinstall preremove postremove; do
    [ -f "$pkg/scripts/$s.sh" ] || fail "packaging/scripts/$s.sh is missing"
done
ok "the unit, the example environment and the four maintainer scripts exist"

for want in \
    'package_name: cirak' \
    'formats:' \
    '- deb' \
    '- rpm' \
    'bindir: /usr/bin' \
    'dst: /usr/lib/systemd/system/cirak.service' \
    'dst: /etc/cirak/cirak.env.example' \
    'type: config|noreplace' \
    'dst: /usr/share/doc/cirak/' \
    'preinstall: packaging/scripts/preinstall.sh' \
    'postinstall: packaging/scripts/postinstall.sh' \
    'preremove: packaging/scripts/preremove.sh' \
    'postremove: packaging/scripts/postremove.sh'; do
    grep -Fq -- "$want" "$gr" || fail ".goreleaser.yaml lacks: $want"
done
ok ".goreleaser.yaml declares the deb/rpm contents, the noreplace config and the scripts"

# ---- the scripts, against a fake systemctl ----------------------------------------

sandbox=$(mktemp -d "${TMPDIR:-/tmp}/cirak-scripts-test.XXXXXX")
trap 'rm -rf "$sandbox"' EXIT HUP INT TERM
mkdir -p "$sandbox/bin"
cat >"$sandbox/bin/systemctl" <<'EOF'
#!/bin/sh
printf '%s\n' "$*" >>"$SYSTEMCTL_LOG"
exit 0
EOF
chmod 0755 "$sandbox/bin/systemctl"
export PATH="$sandbox/bin:$PATH"
export SYSTEMCTL_LOG="$sandbox/calls"

: >"$SYSTEMCTL_LOG"
for arg in upgrade failed-upgrade deconfigure 1 2 3; do
    sh "$pkg/scripts/preremove.sh" "$arg"
    if grep -q 'stop' "$SYSTEMCTL_LOG"; then
        fail "preremove stopped the unit on '$arg' (an upgrade must not stop it)"
    fi
done
ok "preremove does nothing for deb upgrade/failed-upgrade/deconfigure and rpm 1+"

: >"$SYSTEMCTL_LOG"
for arg in remove 0; do
    sh "$pkg/scripts/preremove.sh" "$arg"
    grep -q '^stop cirak.service$' "$SYSTEMCTL_LOG" || fail "preremove did not stop on '$arg'"
    : >"$SYSTEMCTL_LOG"
done
ok "preremove stops only for deb remove and rpm 0"

: >"$SYSTEMCTL_LOG"
sh "$pkg/scripts/postinstall.sh" >/dev/null
grep -q '^daemon-reload$' "$SYSTEMCTL_LOG" || fail "postinstall did not run daemon-reload"
if grep -Eq '^(start|stop|restart|try-restart|reload-or-restart|enable|disable) ' "$SYSTEMCTL_LOG"; then
    cat "$SYSTEMCTL_LOG" >&2
    fail "postinstall started, enabled, stopped or restarted the unit"
fi
ok "postinstall runs daemon-reload and never starts, enables, stops or restarts"

: >"$SYSTEMCTL_LOG"
for arg in remove 0 upgrade 1; do
    sh "$pkg/scripts/postremove.sh" "$arg"
    [ ! -s "$SYSTEMCTL_LOG" ] || fail "postremove called systemctl on '$arg'"
done
grep -Fq 'rm -rf /etc/cirak' "$pkg/scripts/postremove.sh" || fail "postremove does not purge /etc/cirak"
if grep -Eq 'systemctl[[:space:]]+(start|stop|restart|enable)' "$pkg/scripts/postremove.sh"; then
    fail "postremove contains a start/stop/restart/enable call"
fi
ok "postremove never calls systemctl; its purge branch removes only /etc/cirak"

printf 'PASS: packaging file and maintainer-script checks\n'
