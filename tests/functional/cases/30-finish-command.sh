#!/bin/sh
# Test: finish-command runs after process exits with exit code and signal args.
# Validates: finish-command execution, argument passing (exit code + signal).

# Service starts and exits with code 42 after 2s
wait_for_service "finish-svc" "STARTED" 10

# Wait for the service to exit and finish-command to run
sleep 5

# Check that finish-command wrote its result
assert_eq "$(cat /tmp/finish-result 2>/dev/null | head -1)" "finish-command: exit=42 signal=0" \
    "finish-command ran with correct exit code"

test_summary
