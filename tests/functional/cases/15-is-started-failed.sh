#!/bin/sh
# Test: is-started and is-failed exit code behavior.
# Validates: cmdIsStarted (exit 0 if started, 1 otherwise),
#            cmdIsFailed (exit 0 if failed, 1 otherwise).

wait_for_service "ok-svc" "STARTED" 10

# is-started on a running service should exit 0
assert_exit_code "slinitctl --system is-started ok-svc" 0 "is-started ok-svc exits 0"

# is-failed on a running service should exit 1 (not failed)
assert_exit_code "slinitctl --system is-failed ok-svc" 1 "is-failed ok-svc exits 1"

# fail-svc is scripted with `exit 1`; poll for STOPPED instead of
# a fixed sleep so the test stays deterministic under boot-queue
# pressure (`wait_for_service` polls every second up to 10s).
wait_for_service "fail-svc" "STOPPED" 10

# is-started on a failed service should exit 1
assert_exit_code "slinitctl --system is-started fail-svc" 1 "is-started fail-svc exits 1"

# is-failed on a failed service should exit 0
assert_exit_code "slinitctl --system is-failed fail-svc" 0 "is-failed fail-svc exits 0"

test_summary
