#!/bin/sh
# Test: condition-fraction = TAG:PERCENT staged fleet rollout.
# Bucket is FNV-1a(machine-id ++ tag) in [0.00, 99.99).
#   PERCENT=0   → bucket < 0 never holds → condition fails → service
#                 reaches STARTED but no process runs (skipped).
#   PERCENT=100 → bucket < 100 always holds → condition succeeds →
#                 service starts normally with a live PID.
# Machine-id is stable in the test VM, so both outcomes are
# deterministic regardless of host.

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ ! -s /etc/machine-id ]; then
    echo "SKIP: no /etc/machine-id in test VM"
    test_summary
    return 0
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

test_summary
