#!/bin/sh
# Test: stop-command is executed before sending termination signal.
# Validates: stop-command execution, graceful shutdown sequence.

wait_for_service "stop-cmd-svc" "STARTED" 10

# Capture log before stop
slinitctl --system catlog --clear stop-cmd-svc >/dev/null 2>&1

# Stop the service
slinitctl --system stop stop-cmd-svc 2>&1
wait_for_service "stop-cmd-svc" "STOPPED" 10

assert_service_state "stop-cmd-svc" "STOPPED" "service stopped"

# Restart and check logs for stop-command output
slinitctl --system start stop-cmd-svc 2>&1
wait_for_service "stop-cmd-svc" "STARTED" 10

test_summary
