#!/bin/sh
# Test: dynamic-user = yes allocates a transient UID from the
# [61184, 65519] systemd-style pool at BringUp. Validated by
# reading the child's /proc/PID/status Uid: line and asserting
# the numeric UID falls inside the pool range.

wait_for_service "dyn-svc" "STARTED" 10
assert_service_state "dyn-svc" "STARTED" "dyn-svc reached STARTED"

_pid=""
_i=0
while [ "$_i" -lt 5 ]; do
    _pid=$(slinitctl --system status dyn-svc 2>/dev/null | awk '/PID:/ { print $2; exit }')
    [ -n "$_pid" ] && [ "$_pid" != "0" ] && break
    sleep 0.2
    _i=$((_i + 1))
done

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -z "$_pid" ] || [ "$_pid" = "0" ]; then
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: could not resolve PID for dyn-svc"
    test_summary
    return
fi
echo "OK: dyn-svc pid=$_pid"

# Uid: line is <tab>-separated on some kernels — normalize via awk.
_uid=$(awk '/^Uid:/ { print $2; exit }' "/proc/$_pid/status" 2>/dev/null)

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -z "$_uid" ]; then
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: could not read /proc/$_pid/status Uid line"
    test_summary
    return
fi
echo "OK: real UID = $_uid"

# Pool bounds — hard-coded in pkg/service/uidpool.go. If the range
# ever moves, both this test and that file need updating together.
_min=61184
_max=65519

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$_uid" -ge "$_min" ] && [ "$_uid" -le "$_max" ]; then
    echo "OK: UID $_uid falls inside dynamic-user pool [$_min, $_max]"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: UID $_uid outside dynamic-user pool [$_min, $_max]"
fi

# The allocated UID must NOT already exist as a named account — the
# whole point of the transient pool is that the identity is throwaway.
_named=$(awk -F: -v u="$_uid" '$3 == u { print $1; exit }' /etc/passwd 2>/dev/null)
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -z "$_named" ]; then
    echo "OK: UID $_uid is not present in /etc/passwd (transient)"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: UID $_uid is a named account ($_named) — pool leaked into /etc/passwd?"
fi

# Restart the service — the pool should reallocate. Same UID is
# fine (small pool + prompt release), but the allocation path must
# still succeed after Stopped() → BringUp().
slinitctl --system restart dyn-svc >/dev/null 2>&1
wait_for_service "dyn-svc" "STARTED" 10

_pid2=""
_i=0
while [ "$_i" -lt 5 ]; do
    _pid2=$(slinitctl --system status dyn-svc 2>/dev/null | awk '/PID:/ { print $2; exit }')
    [ -n "$_pid2" ] && [ "$_pid2" != "0" ] && break
    sleep 0.2
    _i=$((_i + 1))
done

_uid2=$(awk '/^Uid:/ { print $2; exit }' "/proc/$_pid2/status" 2>/dev/null)
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -n "$_uid2" ] && [ "$_uid2" -ge "$_min" ] && [ "$_uid2" -le "$_max" ]; then
    echo "OK: after restart, UID $_uid2 still inside pool"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: after restart, UID '$_uid2' not inside pool"
fi

test_summary
