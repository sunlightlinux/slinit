#!/bin/sh
# Test: scripted service runs command and reaches STARTED on exit 0.
# Validates: ScriptedService BringUp, stop-command, state transitions.

wait_for_service "scripted-svc" "STARTED" 10
assert_service_state "scripted-svc" "STARTED" "scripted-svc reached STARTED"

# Verify the command actually ran (it creates a marker file)
assert_eq "$(cat /tmp/scripted-marker 2>/dev/null)" "hello" "start command ran"

# Stop it — should run stop-command
slinitctl --system stop scripted-svc
wait_for_service "scripted-svc" "STOPPED" 10
assert_service_state "scripted-svc" "STOPPED" "scripted-svc stopped"

# Verify stop command ran
assert_eq "$(cat /tmp/scripted-stop-marker 2>/dev/null)" "bye" "stop command ran"

test_summary
