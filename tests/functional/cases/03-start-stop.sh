#!/bin/sh
# Test: start and stop a process service via slinitctl.
# Validates: CmdStartService, CmdStopService, state transitions.

# The test-svc service is injected from 03-start-stop.d/
wait_for_service "test-svc" "STARTED" 10
assert_service_state "test-svc" "STARTED" "test-svc started via dependency"

# Stop it
slinitctl --system stop test-svc
wait_for_service "test-svc" "STOPPED" 10
assert_service_state "test-svc" "STOPPED" "test-svc stopped"

# Start it again
slinitctl --system start test-svc
wait_for_service "test-svc" "STARTED" 10
assert_service_state "test-svc" "STARTED" "test-svc restarted"

test_summary
