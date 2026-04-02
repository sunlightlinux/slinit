#!/bin/sh
# Test: ready-check-command polls until service is actually ready.
# Validates: ready-check-command polling, delayed readiness detection.

# The service creates /tmp/ready-marker after 3s. The ready-check-command
# polls for this file. Service should only reach STARTED after the marker exists.

wait_for_service "ready-svc" "STARTED" 15

# Verify the marker file exists (proves ready-check waited for it)
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -f /tmp/ready-marker ]; then
    echo "OK: ready marker exists when service reached STARTED"
else
    echo "FAIL: ready marker missing — service declared STARTED too early"
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
fi

assert_service_state "ready-svc" "STARTED" "ready-svc reached STARTED via ready-check"

# Verify log output confirms the service ran past the ready point
output=$(slinitctl --system catlog ready-svc 2>&1)
assert_contains "$output" "[ready-svc] ready" "service produced output after ready"

test_summary
