#!/bin/sh
# Test: restart-limit-count puts service into failed state after too many restarts.
# Validates: restart-limit-count, restart-limit-interval, failed state transition.

# ratelimit-svc exits immediately with code 1, restart-delay=0.
# After 3 failures within 30s, it should enter STOPPED/failed state.
sleep 10

# Service should have failed (exceeded restart limit)
assert_exit_code "slinitctl --system is-failed ratelimit-svc" 0 \
    "ratelimit-svc is-failed after exceeding restart limit"

# Verify it attempted at least 3 starts
starts=$(wc -l < /tmp/ratelimit-starts 2>/dev/null || echo 0)
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$starts" -ge 3 ]; then
    echo "OK: service attempted $starts starts (>= 3)"
else
    echo "FAIL: service only attempted $starts starts (expected >= 3)"
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
fi

# Service should NOT be STARTED (it's failed)
_state=$(slinitctl --system status ratelimit-svc 2>/dev/null | grep 'State:' | awk '{print $2}')
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$_state" != "STARTED" ]; then
    echo "OK: ratelimit-svc is not STARTED (state: $_state)"
else
    echo "FAIL: ratelimit-svc should not be STARTED after hitting restart limit"
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
fi

test_summary
