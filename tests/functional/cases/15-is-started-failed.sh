#!/bin/sh
# Test: is-started and is-failed exit code behavior.
# Validates: cmdIsStarted (exit 0 if started, 1 otherwise),
#            cmdIsFailed (exit 0 if failed, 1 otherwise).

wait_for_service "ok-svc" "STARTED" 10

# is-started on a running service should exit 0
assert_exit_code "slinitctl --system is-started ok-svc" 0 "is-started ok-svc exits 0"

# is-failed on a running service should exit 1 (not failed)
assert_exit_code "slinitctl --system is-failed ok-svc" 1 "is-failed ok-svc exits 1"

# fail-svc should have failed to start (scripted with exit 1).
# Wait for it to reach STOPPED state (it starts, runs "exit 1", and fails).
sleep 2

# is-started on a failed service should exit 1
assert_exit_code "slinitctl --system is-started fail-svc" 1 "is-started fail-svc exits 1"

# is-failed on a failed service should exit 0
assert_exit_code "slinitctl --system is-failed fail-svc" 0 "is-failed fail-svc exits 0"

test_summary
