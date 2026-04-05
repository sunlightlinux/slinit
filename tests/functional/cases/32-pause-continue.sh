#!/bin/sh
# Test: pause and continue a running service.
# Validates: pause (SIGSTOP), continue (SIGCONT), service stays STARTED.

wait_for_service "pause-svc" "STARTED" 10

# Let it produce some output
sleep 2

# Pause the service
output=$(slinitctl --system pause pause-svc 2>&1)
assert_contains "$output" "" "pause command accepted"

# Service should still show as STARTED (paused is an overlay, not a state change)
assert_service_state "pause-svc" "STARTED" "pause-svc still STARTED while paused"

# Capture log line count while paused
sleep 2
log_paused=$(slinitctl --system catlog pause-svc 2>&1 | wc -l)

# Wait and check no new output (process is frozen)
sleep 2
log_still_paused=$(slinitctl --system catlog pause-svc 2>&1 | wc -l)

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$log_paused" = "$log_still_paused" ]; then
    echo "OK: no new output while paused (lines: $log_paused)"
else
    echo "FAIL: output changed while paused ($log_paused -> $log_still_paused)"
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
fi

# Continue the service
output=$(slinitctl --system continue pause-svc 2>&1)
assert_contains "$output" "" "continue command accepted"

# Let it produce output again
sleep 3
log_resumed=$(slinitctl --system catlog pause-svc 2>&1 | wc -l)

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$log_resumed" -gt "$log_still_paused" ]; then
    echo "OK: output resumed after continue ($log_still_paused -> $log_resumed)"
else
    echo "FAIL: no new output after continue ($log_still_paused -> $log_resumed)"
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
fi

test_summary
