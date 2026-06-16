#!/bin/sh
# 03-essential-services — read-only check that the expected daemons made it
# to STARTED. Subset reflects the slinit + sunlight-os reference install
# (socklog/sshd/crond/dbus/elogind/udevd). Each is checked individually so a
# missing one yields a specific FAIL, not a vague aggregate.

ESSENTIALS="boot sshd crond socklog dbus elogind udevd"

for svc in $ESSENTIALS; do
    assert_service_state "$svc" "STARTED" "$svc is STARTED"
done

# sshd in particular must have a live PID (we are inside it).
_sshd_pid="$(slinitctl --system status sshd 2>/dev/null | awk '/PID:/ {print $2; exit}')"
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -n "$_sshd_pid" ] && [ "$_sshd_pid" -gt 1 ] 2>/dev/null \
   && [ -d "/proc/$_sshd_pid" ]; then
    echo "OK: sshd pid $_sshd_pid is alive"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: sshd has no live PID (got '$_sshd_pid')"
fi

# boot-time output must mention the boot milestone.
_bt="$(slinitctl --system boot-time 2>&1)"
assert_contains "$_bt" "boot" "boot-time mentions boot"
assert_contains "$_bt" "Startup finished" "boot-time has summary line"

test_summary
