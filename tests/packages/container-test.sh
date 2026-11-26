#!/bin/sh
# ADR-026 S4 container install test. Runs as root inside a disposable container
# whose PID 1 is systemd; the host systemd is never touched.
#
# Usage: container-test.sh deb|rpm <v1-package> <v2-package>
# The packages and this tests directory are mounted read-only.
set -eu

kind=${1:?usage: container-test.sh deb|rpm v1-package v2-package}
mount=/pkg
tests=/tests
resolve() {
    case "$1" in
        /*) printf '%s\n' "$1" ;;
        *) printf '%s/%s\n' "$mount" "$1" ;;
    esac
}
v1=$(resolve "${2:?missing v1 package}")
v2=$(resolve "${3:?missing v2 package}")

ok() { printf 'ok - %s\n' "$*"; }
fail() { printf 'not ok - %s\n' "$*" >&2; exit 1; }

expect_file() {
    path=$1 mode=$2 owner=$3
    [ -f "$path" ] || fail "$path is missing"
    got_mode=$(stat -c '%a' "$path")
    got_owner=$(stat -c '%U:%G' "$path")
    [ "$got_mode" = "$mode" ] || fail "$path mode is $got_mode, want $mode"
    [ "$got_owner" = "$owner" ] || fail "$path owner is $got_owner, want $owner"
}

wait_for_state() {
    want=$1
    i=0
    while [ "$i" -lt 60 ]; do
        state=$(systemctl is-active cirak.service || true)
        [ "$state" = "$want" ] && return 0
        i=$((i + 1))
        sleep 1
    done
    fail "cirak.service did not reach $want (is-active=$state)"
}

install_pkg() {
    case "$kind" in
        deb)
            DEBIAN_FRONTEND=noninteractive dpkg -i "$1" >/tmp/install.log 2>&1 || {
                cat /tmp/install.log >&2
                fail "dpkg -i $1 failed"
            }
            ;;
        rpm)
            rpm -Uvh "$1" >/tmp/install.log 2>&1 || {
                cat /tmp/install.log >&2
                fail "rpm -Uvh $1 failed"
            }
            ;;
    esac
}

remove_pkg() {
    case "$kind" in
        deb) dpkg -r cirak >/tmp/remove.log 2>&1 || { cat /tmp/remove.log >&2; fail "dpkg -r failed"; } ;;
        rpm) rpm -e cirak >/tmp/remove.log 2>&1 || { cat /tmp/remove.log >&2; fail "rpm -e failed"; } ;;
    esac
}

# ---- install creates the user and the files with the expected modes ----------------

# The official Debian "slim" image excludes /usr/share/doc at the dpkg level; a
# normal Debian system does not. Remove that container-only exclusion so the
# install check sees the real package payload.
if [ "$kind" = deb ] && [ -f /etc/dpkg/dpkg.cfg.d/docker ]; then
    rm -f /etc/dpkg/dpkg.cfg.d/docker
fi

install_pkg "$v1"

getent group cirak >/dev/null || fail "the cirak group was not created"
user_line=$(getent passwd cirak || true)
[ -n "$user_line" ] || fail "the cirak user was not created"
user_shell=$(printf '%s\n' "$user_line" | cut -d: -f7)
user_home=$(printf '%s\n' "$user_line" | cut -d: -f6)
case "$user_shell" in
    */nologin|*/false) ;;
    *) fail "the cirak user has a login shell: $user_shell" ;;
esac
[ "$user_home" = /nonexistent ] || fail "the cirak user has a home: $user_home"
ok "user and group cirak created without a login shell or a home"

expect_file /usr/bin/cirak 755 root:root
expect_file /usr/lib/systemd/system/cirak.service 644 root:root
expect_file /etc/cirak/cirak.env.example 644 root:root
expect_file /usr/share/doc/cirak/LICENSE 644 root:root
expect_file /usr/share/doc/cirak/README.md 644 root:root
[ -f /usr/share/doc/cirak/SYSTEMD_LOCAL_VALIDATION.md ] || fail "the deploy runbooks were not packaged"
ok "the binary, unit, config example and docs have the expected modes"

grep -qx 'Restart=no' /usr/lib/systemd/system/cirak.service || fail "the packaged unit is not Restart=no"
[ "$(systemctl show -p Restart --value cirak.service)" = no ] || fail "systemctl does not see Restart=no"
systemd-analyze verify /usr/lib/systemd/system/cirak.service 2>/tmp/verify.log || {
    cat /tmp/verify.log >&2
    fail "systemd-analyze verify rejected the unit"
}
ok "systemd-analyze verify accepts the unit and Restart=no is visible"

[ "$(systemctl is-enabled cirak.service 2>/dev/null || true)" = disabled ] || {
    fail "the package must not enable the unit"
}
ok "the package left the unit disabled"

# The unit expects this file; it is operator-owned, so the test creates it.
install -m 0600 /dev/null /etc/cirak/token
printf 'package-test-token\n' >/etc/cirak/token

# ---- (a) a non-zero exit is not restarted ------------------------------------------

install -m 0755 "$tests/fixtures/exit-nonzero.sh" /usr/bin/cirak
systemctl start cirak.service || true
wait_for_state failed
[ "$(systemctl show -p NRestarts --value cirak.service)" = 0 ] || fail "a failed service was restarted"
[ "$(systemctl show -p Restart --value cirak.service)" = no ] || fail "Restart is not no at runtime"
ok "(a) a non-zero exit leaves the unit failed with NRestarts=0"
systemctl reset-failed cirak.service

# ---- (b) an upgrade keeps the unit active with the same main PID ------------------

install -m 0755 "$tests/fixtures/hold.sh" /usr/bin/cirak
printf '\n# package-test-upgrade-marker\n' >>/etc/cirak/cirak.env.example
systemctl start cirak.service
wait_for_state active
pid_before=$(systemctl show -p MainPID --value cirak.service)
[ "$pid_before" -gt 0 ] || fail "the service has no main PID"

for dir in /var/lib/cirak /var/lib/cirak/overlay /var/lib/cirak/writer-locks /run/cirak-admin /run/cirak-dashboard; do
    [ -d "$dir" ] || fail "the unit did not create $dir"
    [ "$(stat -c '%a' "$dir")" = 700 ] || fail "$dir mode is $(stat -c '%a' "$dir"), want 700"
    [ "$(stat -c '%U:%G' "$dir")" = cirak:cirak ] || fail "$dir owner is $(stat -c '%U:%G' "$dir"), want cirak:cirak"
done
ok "RuntimeDirectory and StateDirectory exist with mode 0700 and owner cirak:cirak"

install_pkg "$v2"

[ "$(systemctl is-active cirak.service)" = active ] || fail "the upgrade stopped the unit"
pid_after=$(systemctl show -p MainPID --value cirak.service)
[ "$pid_after" = "$pid_before" ] || fail "the upgrade changed the main PID ($pid_before -> $pid_after)"
[ "$(systemctl show -p NRestarts --value cirak.service)" = 0 ] || fail "the upgrade restarted the unit"
grep -q 'package-test-upgrade-marker' /etc/cirak/cirak.env.example || fail "the upgrade replaced the conffile"
ok "(b) the upgrade kept the unit active with PID $pid_before and the edited config file"

# ---- (c) removal stops it and keeps state and the user -----------------------------

mkdir -p /var/lib/cirak
printf 'package-test-state\n' >/var/lib/cirak/package-test-state
remove_pkg
i=0
while [ "$i" -lt 30 ]; do
    state=$(systemctl is-active cirak.service || true)
    case "$state" in
        inactive|failed|unknown) break ;;
    esac
    i=$((i + 1))
    sleep 1
done
case "$state" in
    inactive|failed|unknown) ;;
    *) fail "the removal did not stop the unit (is-active=$state)" ;;
esac
ok "the removal stopped the unit"

[ -f /var/lib/cirak/package-test-state ] || fail "the removal deleted state"
[ -d /var/lib/cirak/overlay ] || fail "the removal deleted the overlay state directory"
[ -d /var/lib/cirak/writer-locks ] || fail "the removal deleted the writer-lock directory"
getent passwd cirak >/dev/null || fail "the removal deleted the cirak user"
ok "the removal kept the state directories and the user"

case "$kind" in
    deb)
        [ -f /etc/cirak/cirak.env.example ] || fail "deb remove deleted the conffile"
        dpkg -P cirak >/tmp/purge.log 2>&1 || { cat /tmp/purge.log >&2; fail "dpkg -P failed"; }
        [ ! -e /etc/cirak ] || fail "purge did not remove /etc/cirak"
        [ -f /var/lib/cirak/package-test-state ] || fail "purge deleted state"
        getent passwd cirak >/dev/null || fail "purge deleted the cirak user"
        ok "purge removed only /etc/cirak; state and user stayed"
        ;;
    rpm)
        # rpm has no purge: erase removes the packaged config file, but never the
        # state directories or the user.
        [ ! -e /etc/cirak/cirak.env.example ] || fail "rpm erase left the packaged config file"
        [ -f /var/lib/cirak/package-test-state ] || fail "rpm erase deleted state"
        getent passwd cirak >/dev/null || fail "rpm erase deleted the cirak user"
        ok "rpm erase removed its config file; state and user stayed (rpm has no purge)"
        ;;
esac

printf 'PASS: %s package test complete\n' "$kind"
