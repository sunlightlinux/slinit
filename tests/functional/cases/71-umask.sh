#!/bin/sh
# Test: per-service umask is applied to the service process.
# Validates: umask = config option.

wait_for_service "umask-svc" "STARTED" 10

# The scripted service runs "umask > /tmp/umask-marker" with umask = 0077,
# so the child's reported mask must be 0077 (busybox ash prints it with a
# leading zero).
assert_eq "$(cat /tmp/umask-marker 2>/dev/null)" "0077" "service umask is 0077"

test_summary
