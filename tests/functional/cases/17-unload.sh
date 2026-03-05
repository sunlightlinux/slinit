#!/bin/sh
# Test: unload a stopped service from memory.
# Validates: CmdUnloadService removes service from the service set.

wait_for_service "unload-svc" "STARTED" 10

# Stop the service first
slinitctl --system stop unload-svc
wait_for_service "unload-svc" "STOPPED" 10
assert_service_state "unload-svc" "STOPPED" "unload-svc stopped"

# Remove the waits-for dependency from boot so unload can succeed
# (a service with dependents cannot be unloaded)
slinitctl --system rm-dep boot waits-for unload-svc

# Unload it
output=$(slinitctl --system unload unload-svc 2>&1)
assert_contains "$output" "unloaded" "unload command succeeded"

# Verify it's no longer in the service list
list=$(slinitctl --system list 2>&1)
assert_not_contains "$list" "unload-svc" "unload-svc removed from list"

test_summary
