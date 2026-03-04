#!/bin/sh
# Test: slinit boots successfully and the boot service reaches STARTED.
# Validates: PID 1 init, service loading, dependency resolution, boot milestone.

# When booted via /sbin/init symlink, comm shows "init"; verify it's our binary
_comm=$(cat /proc/1/comm)
_TESTS_RUN=$((_TESTS_RUN + 1))
case "$_comm" in
    slinit|init)
        echo "OK: PID 1 is $_comm (slinit binary)"
        ;;
    *)
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: PID 1 is '$_comm', expected slinit or init"
        ;;
esac

# Verify it's actually slinit by checking the control socket exists
assert_eq "$(test -S /run/slinit.socket && echo yes || echo no)" "yes" "slinit control socket exists"

wait_for_service "boot" "STARTED" 10
assert_service_state "boot" "STARTED" "boot service is STARTED"
assert_service_state "system-init" "STARTED" "system-init is STARTED"

# /proc and /sys should be mounted by system-init
assert_eq "$(mountpoint -q /proc && echo yes || echo no)" "yes" "/proc is mounted"
assert_eq "$(mountpoint -q /sys && echo yes || echo no)" "yes" "/sys is mounted"

test_summary
