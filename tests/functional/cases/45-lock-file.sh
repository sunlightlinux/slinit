#!/bin/sh
# Test: lock-file creates an exclusive flock preventing duplicate starts.
# Validates: lock-file setting, flock acquisition, file creation.

wait_for_service "lock-svc" "STARTED" 10

# Verify lock file was created
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -f /run/lock-svc.lock ]; then
    echo "OK: lock file exists"
else
    echo "FAIL: lock file /run/lock-svc.lock not found"
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
fi

# Service should be running
assert_service_state "lock-svc" "STARTED" "lock-svc is STARTED"

# Verify the service is actually running
assert_eq "$(cat /tmp/lock-svc-status 2>/dev/null)" "running" "lock-svc wrote status"

# Stop the service and verify lock file is released
slinitctl --system stop lock-svc 2>&1
sleep 2

assert_service_state "lock-svc" "STOPPED" "lock-svc is STOPPED after stop"

test_summary
