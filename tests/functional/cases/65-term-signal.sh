#!/bin/sh
# Test: term-signal sends a custom signal instead of SIGTERM on stop.
# Validates: term-signal config, custom stop signal delivery.

wait_for_service "termsig-svc" "STARTED" 10

# Stop the service — should send SIGINT instead of SIGTERM
slinitctl --system stop termsig-svc 2>&1
sleep 3

# Verify the service received SIGINT (not SIGTERM)
result=$(cat /tmp/termsig-result 2>/dev/null)
assert_eq "$result" "INT-received" "service received SIGINT on stop"

# Service should be stopped
assert_service_state "termsig-svc" "STOPPED" "termsig-svc is STOPPED"

test_summary
