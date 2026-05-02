#!/bin/sh
# Test: smooth-recovery restarts without propagating failure to dependents.
# Validates: smooth-recovery=yes, dependent stays STARTED during crash-restart.

wait_for_service "smooth-svc" "STARTED" 10
wait_for_service "dep-svc" "STARTED" 10

# smooth-svc exits after 2s, restarts with 1s delay.
# With smooth-recovery, dep-svc should NOT be stopped/restarted.

# Record dep-svc PID before the crash
dep_pid1=$(slinitctl --system status dep-svc 2>/dev/null | grep 'PID:' | awk '{print $2}')

# Wait for smooth-svc to crash and restart
sleep 5

# smooth-svc should have restarted (check restart count via timestamps)
starts=$(wc -l < /tmp/smooth-starts 2>/dev/null || echo 0)
assert_eq "$([ "$starts" -ge 2 ] && echo yes || echo no)" "yes" \
    "smooth-svc restarted at least once (starts=$starts)"

# dep-svc should still be STARTED (not affected by smooth-svc crash)
assert_service_state "dep-svc" "STARTED" "dep-svc still STARTED (smooth recovery)"

# dep-svc PID should be unchanged (it was not restarted)
dep_pid2=$(slinitctl --system status dep-svc 2>/dev/null | grep 'PID:' | awk '{print $2}')
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -n "$dep_pid1" ] && [ -n "$dep_pid2" ] && [ "$dep_pid1" = "$dep_pid2" ]; then
    echo "OK: dep-svc PID unchanged ($dep_pid1) — not restarted"
else
    echo "FAIL: dep-svc PID changed ($dep_pid1 -> $dep_pid2) — was restarted"
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
fi

test_summary
