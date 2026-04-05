#!/bin/sh
# Test: cron-like periodic task execution.
# Validates: cron-command, cron-interval, cron-delay, periodic execution.

wait_for_service "cron-svc" "STARTED" 10

# Wait for at least 2 cron ticks (1s delay + 2x 2s interval = ~5s)
sleep 6

# Check that ticks were recorded
ticks=$(wc -l < /tmp/cron-ticks 2>/dev/null || echo 0)
assert_eq "$([ "$ticks" -ge 2 ] && echo yes || echo no)" "yes" \
    "cron ran at least 2 times (got $ticks ticks)"

# Verify service is still running (cron doesn't stop the service)
assert_service_state "cron-svc" "STARTED" "service still running after cron ticks"

test_summary
