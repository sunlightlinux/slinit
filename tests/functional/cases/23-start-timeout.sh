#!/bin/sh
# Test: start-timeout kills a service that takes too long to start.
# Validates: StartTimeout, service transitions to STOPPED on timeout.

# timeout-svc is a scripted service with start-timeout=3 and command that sleeps 120s.
# It should time out and end up STOPPED.
sleep 6

# After timeout, service should be STOPPED (failed to start in time)
assert_service_state "timeout-svc" "STOPPED" "timeout-svc STOPPED after timeout"

# is-failed should report it as failed
assert_exit_code "slinitctl --system is-failed timeout-svc" 0 "timeout-svc is failed"

test_summary
