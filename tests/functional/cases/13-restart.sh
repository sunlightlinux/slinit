#!/bin/sh
# Test: slinitctl restart command (stop + start cycle).
# Validates: cmdRestart, PID changes, service returns to STARTED.

wait_for_service "rsvc" "STARTED" 10

pid1=$(slinitctl --system status rsvc 2>/dev/null | grep 'PID:' | awk '{print $2}')

output=$(slinitctl --system restart rsvc 2>&1)
assert_contains "$output" "restarted" "restart command succeeded"

wait_for_service "rsvc" "STARTED" 10
assert_service_state "rsvc" "STARTED" "rsvc STARTED after restart"

pid2=$(slinitctl --system status rsvc 2>/dev/null | grep 'PID:' | awk '{print $2}')
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -n "$pid1" ] && [ -n "$pid2" ] && [ "$pid1" != "$pid2" ]; then
    echo "OK: PID changed ($pid1 -> $pid2)"
else
    echo "FAIL: PID did not change ($pid1 -> $pid2)"
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
fi

test_summary
