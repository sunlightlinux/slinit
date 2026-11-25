#!/bin/sh
# Test: timeout cluster (timeout-sec / timeout-abort-sec /
# timeout-start-failure-mode / timeout-stop-failure-mode) parses and
# drives the stop escalation.
#
# The service ignores SIGTERM. Stopping it must wait timeout-sec (2 s),
# send SIGABRT, which the service records, wait timeout-abort-sec (1 s)
# and SIGKILL it: about 3 s in all. Without timeout-sec the default
# 10 s stop timeout applies; without timeout-abort-sec there is no
# SIGABRT. (The case used to check only that the service started.)

wait_for_service "tocluster-svc" "STARTED" 10
assert_service_state "tocluster-svc" "STARTED" "timeout cluster parses + service starts"

t0=$(cut -d' ' -f1 /proc/uptime)
slinitctl --system stop tocluster-svc >/dev/null 2>&1
t1=$(cut -d' ' -f1 /proc/uptime)
took=$(awk -v a="$t0" -v b="$t1" 'BEGIN { printf "%.2f", b - a }')
echo "INFO: stop took ${took}s"

assert_service_state "tocluster-svc" "STOPPED" "service stopped"
_TESTS_RUN=$((_TESTS_RUN + 1))
if awk -v t="$took" 'BEGIN { exit !(t >= 2.5 && t < 6) }'; then
    echo "OK: stop took ${took}s (timeout-sec 2 + timeout-abort-sec 1)"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: stop took ${took}s, expected about 3s"
fi
assert_eq "$(grep -c abrt /run/183-abrt 2>/dev/null)" "1" "SIGABRT phase reached the service once"

test_summary
