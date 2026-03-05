#!/bin/sh
# Test: shutdown command is accepted via control socket.
# Validates: the shutdown protocol path works.
#
# We cannot actually call "shutdown poweroff" because it would kill the
# VM before guest-runner writes results. Instead we verify the command
# is reachable by passing an invalid shutdown type.

# Verify boot is up
wait_for_service "boot" "STARTED" 10
assert_service_state "boot" "STARTED" "boot is STARTED"

# Verify services are running
list=$(slinitctl --system list 2>&1)
assert_contains "$list" "boot" "boot in service list"

# Call shutdown with an invalid type to prove the command path works
# without actually shutting down the VM
output=$(slinitctl --system shutdown invalid-type 2>&1 || true)
assert_contains "$output" "unknown shutdown type" "shutdown command reachable"

test_summary
