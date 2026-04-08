#!/bin/sh
# Test: pre-stop-hook runs before service is stopped.
# Validates: pre-stop-hook execution ordering.

wait_for_service "hook-svc" "STARTED" 10

# Stop the service — pre-stop-hook should run first
slinitctl --system stop hook-svc 2>&1

# Wait for service to reach STOPPED
sleep 3

# Verify pre-stop-hook wrote its marker
result=$(cat /tmp/pre-stop-result 2>/dev/null)
assert_eq "$result" "pre-stop-ran" "pre-stop-hook executed before stop"

# Service should be stopped now
assert_service_state "hook-svc" "STOPPED" "hook-svc is STOPPED after stop"

test_summary
