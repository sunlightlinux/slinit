#!/bin/sh
# Test: systemd-style seccomp-bpf filter (#4).
# Validates:
#   - @system-service group is broad enough that an ordinary shell
#     service starts and runs normally;
#   - a denylist on mount actually blocks it: the root service's
#     `mount -t tmpfs` is refused with EPERM and no tmpfs named
#     seccomp-probe reaches /proc/mounts.
#
# This used to probe ptrace with strace, which the guest does not ship,
# so it reported a "soft skip" on every run and tested nothing. mount is
# probeable with busybox alone, and the service is root with full caps,
# so without the filter the mount succeeds and this case fails.
# (busybox mount turns EPERM into "permission denied (are you root?)";
# the probe accepts that or the plain strerror text.)

wait_for_service "seccomp-allowed" "STARTED" 15
wait_for_service "seccomp-blocked" "STARTED" 15

# Marker proves the allowed service's command body ran. If seccomp had
# wrongly killed it, no marker would be written.
[ -f /tmp/seccomp-allowed-marker ] && allowed=yes || allowed=no
assert_eq "$allowed" "yes" "@system-service allows ordinary shell to run"

# The body runs after STARTED for a type=process service — give it a
# moment to write its witness.
for _ in 1 2 3 4 5 6 7 8 9 10; do
    [ -s /tmp/seccomp-blocked-result ] && break
    sleep 0.5
done

result=$(cat /tmp/seccomp-blocked-result 2>/dev/null)
err=$(cat /run/seccomp-mount-err 2>/dev/null)
echo "  diag: result='$result' mount stderr='$err'"
assert_eq "$result" "mount-refused" "denylist on mount refuses mount(2)"

_TESTS_RUN=$((_TESTS_RUN + 1))
case "$err" in
*"not permitted"*|*"ermission denied"*)
    echo "OK: mount refused with EPERM, not some other error" ;;
*)
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: mount refusal was not EPERM: '$err'" ;;
esac

# The authoritative check: nothing got mounted, whatever the probe said.
_mounted=$(grep -c ' /run/seccomp-mnt ' /proc/mounts)
assert_eq "${_mounted:-0}" "0" "no seccomp-probe tmpfs reached /proc/mounts"

test_summary
