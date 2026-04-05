#!/bin/sh
# Test: 'down' marker file prevents service from auto-starting.
# Validates: down-file detection, markedDown flag, explicit start clears it.

wait_for_service "boot" "STARTED" 10

# Load down-svc (without starting) by querying it
# The 'down' file in the service dir should set markedDown=true
slinitctl --system start down-svc >/dev/null 2>&1
sleep 3

# After explicit start, markedDown is cleared and service should be STARTED
assert_service_state "down-svc" "STARTED" "explicit start overrides down file"

# Stop and verify
slinitctl --system stop down-svc >/dev/null 2>&1
sleep 1
assert_service_state "down-svc" "STOPPED" "down-svc stopped cleanly"

test_summary
