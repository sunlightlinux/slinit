#!/bin/sh
# Test: orderly shutdown via control socket.
# Validates: CmdShutdown with poweroff type.
# Note: this test triggers shutdown, so it must be the last action.

# Verify boot is up
wait_for_service "boot" "STARTED" 10
assert_service_state "boot" "STARTED" "boot is STARTED"

# List services to confirm things are running
list=$(slinitctl --system list 2>&1)
assert_contains "$list" "boot" "boot in service list"

# The shutdown command is tested implicitly by every test (guest-runner
# calls shutdown at the end). Here we verify the command output.
output=$(slinitctl --system shutdown poweroff 2>&1)
assert_contains "$output" "initiated\|Shutdown" "shutdown command accepted"

test_summary
