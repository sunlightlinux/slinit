#!/bin/sh
# Test: control-command runs a custom handler instead of delivering a signal.
# Validates: control-command-HUP, signal interception.

wait_for_service "ctrl-svc" "STARTED" 10

# Send HUP — should invoke control-command-HUP instead of signal delivery
slinitctl --system signal HUP ctrl-svc 2>&1

# Wait for handler to execute
sleep 2

# Verify custom handler ran
result=$(cat /tmp/ctrl-cmd-result 2>/dev/null)
assert_eq "$result" "hup-handled" "control-command-HUP executed"

# Service should still be running (signal was intercepted, not delivered)
assert_service_state "ctrl-svc" "STARTED" "ctrl-svc still STARTED after HUP"

test_summary
