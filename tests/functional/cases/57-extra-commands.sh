#!/bin/sh
# Test: extra-command and extra-started-command define custom actions.
# Validates: extra-command (any state), extra-started-command (started only),
#            slinitctl action invocation, slinitctl list-actions.

wait_for_service "action-svc" "STARTED" 10

# List available actions
actions=$(slinitctl --system list-actions action-svc 2>&1)
assert_contains "$actions" "dump" "dump action listed"
assert_contains "$actions" "status" "status action listed"

# Run the extra-command "dump" (callable in any state)
slinitctl --system action action-svc dump 2>&1
sleep 1
result=$(cat /tmp/action-dump 2>/dev/null)
assert_eq "$result" "dump-ran" "extra-command dump executed"

# Run the extra-started-command "status" (only when started)
slinitctl --system action action-svc status 2>&1
sleep 1
result2=$(cat /tmp/action-status 2>/dev/null)
assert_eq "$result2" "status-ran" "extra-started-command status executed"

# Stop the service and try the started-only action (should fail)
slinitctl --system stop action-svc 2>&1
wait_for_service "action-svc" "STOPPED" 10

# Clear the old marker to detect if the action runs again
echo "old" > /tmp/action-status
slinitctl --system action action-svc status 2>&1 || true
sleep 1

# The started-only action should NOT have run (service is stopped)
result3=$(cat /tmp/action-status 2>/dev/null)
assert_eq "$result3" "old" "extra-started-command did not run when stopped"

test_summary
