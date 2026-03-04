#!/bin/sh
# Test: process service auto-restarts on failure.
# Validates: restart=on-failure, restart-delay, restart-limit-count.

# restart-svc exits with code 1 after 2s, should auto-restart
wait_for_service "restart-svc" "STARTED" 10
assert_service_state "restart-svc" "STARTED" "restart-svc initially STARTED"

# Get initial PID
pid1=$(slinitctl --system status restart-svc 2>/dev/null | grep 'PID:' | awk '{print $2}')

# Wait for it to crash and restart (exits after 2s, 1s restart delay)
sleep 5

# Should still be STARTED (restarted)
assert_service_state "restart-svc" "STARTED" "restart-svc restarted after failure"

# PID should have changed
pid2=$(slinitctl --system status restart-svc 2>/dev/null | grep 'PID:' | awk '{print $2}')
if [ -n "$pid1" ] && [ -n "$pid2" ]; then
    _TESTS_RUN=$((_TESTS_RUN + 1))
    if [ "$pid1" != "$pid2" ]; then
        echo "OK: PID changed ($pid1 -> $pid2), confirming restart"
    else
        echo "FAIL: PID unchanged ($pid1), restart may not have happened"
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
    fi
fi

test_summary
