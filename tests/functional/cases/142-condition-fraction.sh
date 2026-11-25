#!/bin/sh
# Test: condition-fraction = TAG:PERCENT staged fleet rollout.
# Bucket is FNV-1a(machine-id ++ tag) in [0.00, 99.99).
#   PERCENT=0   → bucket < 0 never holds → condition fails → service
#                 reaches STARTED but no process runs (skipped).
#   PERCENT=100 → bucket < 100 always holds → condition succeeds →
#                 service starts normally with a live PID.
# 0 and 100 do not depend on the machine-id at all, so the case seeds
# /etc/machine-id (rootfs/) and adds two services either side of the
# bucket that id gives tag "stage1": 46.22%. stage1:46 must skip and
# stage1:47 must run, which only holds if the bucket is really hashed
# from the id. (The VM had no machine-id, so the case always skipped.)

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ ! -s /etc/machine-id ]; then
    skip_case "no /etc/machine-id in test VM"
fi
echo "OK: /etc/machine-id present"

wait_for_service "frac-zero" "STARTED" 10
wait_for_service "frac-hundred" "STARTED" 10

# Both services report STARTED — condition failures still land in
# STARTED (silent skip), so state alone doesn't disambiguate.
assert_service_state "frac-zero"    "STARTED" "frac-zero (0%) reached STARTED"
assert_service_state "frac-hundred" "STARTED" "frac-hundred (100%) reached STARTED"

# The distinguishing signal is the PID: skipped services have none.
_pid_zero=$(slinitctl --system status frac-zero 2>/dev/null | awk '/PID:/ { print $2; exit }')
_pid_hundred=$(slinitctl --system status frac-hundred 2>/dev/null | awk '/PID:/ { print $2; exit }')

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -z "$_pid_zero" ] || [ "$_pid_zero" = "0" ] || [ "$_pid_zero" = "-" ]; then
    echo "OK: frac-zero (0%) skipped — no PID"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: frac-zero (0%) has PID=$_pid_zero — condition should have failed"
fi

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -n "$_pid_hundred" ] && [ "$_pid_hundred" != "0" ] && [ "$_pid_hundred" != "-" ] && [ -d "/proc/$_pid_hundred" ]; then
    echo "OK: frac-hundred (100%) started — PID=$_pid_hundred alive"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: frac-hundred (100%) has no live PID (got '$_pid_hundred')"
fi

has_pid() {
    _p=$(slinitctl --system status "$1" 2>/dev/null | awk '/PID:/ { print $2; exit }')
    [ -n "$_p" ] && [ "$_p" != "0" ] && [ "$_p" != "-" ] && [ -d "/proc/$_p" ]
}
wait_for_service "frac-below" "STARTED" 10
wait_for_service "frac-above" "STARTED" 10

_TESTS_RUN=$((_TESTS_RUN + 1))
if has_pid frac-below; then
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: frac-below (stage1:46) runs; the bucket is 46.22"
else
    echo "OK: frac-below (stage1:46) skipped"
fi

_TESTS_RUN=$((_TESTS_RUN + 1))
if has_pid frac-above; then
    echo "OK: frac-above (stage1:47) runs"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: frac-above (stage1:47) did not run; the bucket is 46.22"
fi

test_summary
