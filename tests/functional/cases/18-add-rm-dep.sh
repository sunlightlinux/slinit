#!/bin/sh
# Test: runtime dependency management (add-dep, rm-dep).
# Validates: CmdAddDep, CmdRmDep with depends-on type.

wait_for_service "dep-from" "STARTED" 10
wait_for_service "dep-to" "STARTED" 10

# Add a depends-on dependency: dep-from depends-on dep-to
output=$(slinitctl --system add-dep dep-from depends-on dep-to 2>&1)
assert_contains "$output" "Added" "add-dep succeeded"

# Stopping dep-to should now also stop dep-from (hard dependency)
slinitctl --system stop dep-to
wait_for_service "dep-to" "STOPPED" 10
wait_for_service "dep-from" "STOPPED" 10
assert_service_state "dep-from" "STOPPED" "dep-from stopped due to hard dep"

# Start both back
slinitctl --system start dep-to
wait_for_service "dep-to" "STARTED" 10
slinitctl --system start dep-from
wait_for_service "dep-from" "STARTED" 10

# Remove the dependency
output=$(slinitctl --system rm-dep dep-from depends-on dep-to 2>&1)
assert_contains "$output" "Removed" "rm-dep succeeded"

# Now stopping dep-to should NOT stop dep-from
slinitctl --system stop dep-to
wait_for_service "dep-to" "STOPPED" 10
sleep 1
assert_service_state "dep-from" "STARTED" "dep-from still STARTED after rm-dep"

test_summary
