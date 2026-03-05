#!/bin/sh
# Test: pin start, then unpin to allow stop.
# Validates: --pin flag on start, CmdUnpinService.

wait_for_service "pin-svc" "STARTED" 10

# Stop it first
slinitctl --system stop pin-svc
wait_for_service "pin-svc" "STOPPED" 10

# Start with --pin (pins it in started state)
slinitctl --system --pin start pin-svc
wait_for_service "pin-svc" "STARTED" 10
assert_service_state "pin-svc" "STARTED" "pin-svc started with pin"

# Try to stop — should fail because it's pinned
output=$(slinitctl --system stop pin-svc 2>&1)
assert_contains "$output" "pinned" "stop rejected due to pin"
assert_service_state "pin-svc" "STARTED" "pin-svc still STARTED (pinned)"

# Unpin it
output=$(slinitctl --system unpin pin-svc 2>&1)
assert_contains "$output" "unpinned" "unpin succeeded"

# Now stop should work
slinitctl --system stop pin-svc
wait_for_service "pin-svc" "STOPPED" 10
assert_service_state "pin-svc" "STOPPED" "pin-svc stopped after unpin"

test_summary
