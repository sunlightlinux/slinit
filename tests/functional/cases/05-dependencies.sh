#!/bin/sh
# Test: dependency chain resolution (depends-on, waits-for).
# Validates: transitive deps, ordering, all services reach STARTED.

wait_for_service "dep-leaf" "STARTED" 10
wait_for_service "dep-mid" "STARTED" 10
wait_for_service "dep-root" "STARTED" 10

assert_service_state "dep-leaf" "STARTED" "dep-leaf (no deps) is STARTED"
assert_service_state "dep-mid" "STARTED" "dep-mid (waits-for dep-leaf) is STARTED"
assert_service_state "dep-root" "STARTED" "dep-root (depends-on dep-mid) is STARTED"

# Stopping dep-leaf should not stop dep-mid (waits-for is soft)
slinitctl --system stop dep-leaf
wait_for_service "dep-leaf" "STOPPED" 10
assert_service_state "dep-leaf" "STOPPED" "dep-leaf stopped"
assert_service_state "dep-mid" "STARTED" "dep-mid still STARTED (soft dep)"

# Stopping dep-mid should stop dep-root (depends-on is hard)
slinitctl --system stop dep-mid
wait_for_service "dep-root" "STOPPED" 10
assert_service_state "dep-root" "STOPPED" "dep-root stopped (hard dep on dep-mid)"

test_summary
