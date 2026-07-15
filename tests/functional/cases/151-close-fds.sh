#!/bin/sh
# Test: close-stdin/close-stdout/close-stderr redirect the standard
# fds to /dev/null. Validated by resolving /proc/PID/fd/{0,1,2} and
# checking each symlink targets /dev/null. A regression that made the
# child inherit the daemon socket or the log pipe on those fds would
# leak sensitive state — this test catches that.

wait_for_service "closed-svc" "STARTED" 10
assert_service_state "closed-svc" "STARTED" "closed-svc reached STARTED"

_pid=""
_i=0
while [ "$_i" -lt 5 ]; do
    _pid=$(slinitctl --system status closed-svc 2>/dev/null | awk '/PID:/ { print $2; exit }')
    [ -n "$_pid" ] && [ "$_pid" != "0" ] && break
    sleep 0.2
    _i=$((_i + 1))
done

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -z "$_pid" ] || [ "$_pid" = "0" ]; then
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: could not resolve PID for closed-svc"
    test_summary
    return
fi
echo "OK: closed-svc pid=$_pid"

for fd in 0 1 2; do
    _target=$(readlink "/proc/$_pid/fd/$fd" 2>/dev/null)
    assert_eq "$_target" "/dev/null" "fd $fd points to /dev/null"
done

test_summary
