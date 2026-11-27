#!/bin/sh
# Test: restart-limit-count puts service into failed state after too many restarts.
# Validates: restart-limit-count, restart-limit-interval, failed state transition.

# ratelimit-svc exits immediately with code 1, restart-delay=0.
# After 3 restarts within 30s, its 4th failure puts it into the failed state.
sleep 10

# Service should have failed (exceeded restart limit)
assert_exit_code "slinitctl --system is-failed ratelimit-svc" 0 \
    "ratelimit-svc is-failed after exceeding restart limit"

# restart-limit-count = 3 allows 3 restarts: 4 starts in all. Each exit
# used to be counted twice against the limit (once for the service's own
# restart, once on behalf of its dependents), so it allowed 2 and the
# service ran 3 times; ">= 3" let that pass.
starts=$(wc -l < /tmp/ratelimit-starts 2>/dev/null || echo 0)
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$starts" -eq 4 ]; then
    echo "OK: service started 4 times (3 restarts allowed)"
else
    echo "FAIL: service started $starts times, expected 4 (3 restarts allowed)"
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
