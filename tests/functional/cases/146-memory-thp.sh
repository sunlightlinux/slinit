#!/bin/sh
# Test: memory-thp = never accepts parse, wires through
# slinit-runner, and the target reaches STARTED. When the kernel
# exposes /proc/PID/status THP_enabled we also verify it reads
# "false" — that field only landed in linux 5.11+, so it's a
# best-effort assertion (missing field = SKIP, not FAIL).
#
# The systemd-parity semantics ('madvise'/'always' are accepted for
# config parity but leave the system default) are covered by the
# parser unit test TestParseMemoryTHP. Here we exercise only the
# never branch since that's the one with a runtime effect.

wait_for_service "thp-svc" "STARTED" 10
assert_service_state "thp-svc" "STARTED" "thp-svc reached STARTED with memory-thp=never"

_pid=""
_i=0
while [ "$_i" -lt 5 ]; do
    _pid=$(slinitctl --system status thp-svc 2>/dev/null | awk '/PID:/ { print $2; exit }')
    [ -n "$_pid" ] && [ "$_pid" != "0" ] && break
    sleep 0.2
    _i=$((_i + 1))
done

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -n "$_pid" ] && [ "$_pid" != "0" ] && [ -d "/proc/$_pid" ]; then
    echo "OK: thp-svc has live PID=$_pid (runner path executed)"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: thp-svc has no live PID (got '$_pid')"
    test_summary
    return 0
fi

# Best-effort THP_enabled check. Field format: "THP_enabled:  0".
# Missing field = old kernel; skip rather than fail.
_thp_line=$(awk '/^THP_enabled:/ { print $2; exit }' "/proc/$_pid/status" 2>/dev/null)
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -z "$_thp_line" ]; then
    echo "SKIP: /proc/PID/status lacks THP_enabled field (kernel too old)"
elif [ "$_thp_line" = "0" ]; then
    echo "OK: THP_enabled=0 on child (PR_SET_THP_DISABLE took effect)"
elif [ "$_thp_line" = "1" ]; then
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: THP_enabled=1 — runner did not disable THP on the child"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: unexpected THP_enabled='$_thp_line'"
fi

test_summary
