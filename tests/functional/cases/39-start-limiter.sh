#!/bin/sh
# Test: soft parallel start limit.
# Validates: --parallel-start-limit flag, all services eventually start.
# Note: This test boots slinit with --parallel-start-limit=2, so at most 2
# services start concurrently. All 3 should still reach STARTED.

wait_for_service "svc-a" "STARTED" 15
wait_for_service "svc-b" "STARTED" 15
wait_for_service "svc-c" "STARTED" 15

# All three marker files should exist
for s in a b c; do
    f="/tmp/limit-$s"
    assert_eq "$(test -f "$f" && echo yes || echo no)" "yes" \
        "svc-$s started (marker $f exists)"
done

# Verify all 3 services running
output=$(slinitctl --system list 2>&1)
assert_contains "$output" "svc-a" "svc-a in list"
assert_contains "$output" "svc-b" "svc-b in list"
assert_contains "$output" "svc-c" "svc-c in list"

test_summary
