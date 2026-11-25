#!/bin/sh
# Test: stop-command is executed when the service is stopped.
# Validates: stop-command execution, graceful shutdown sequence.
#
# The stop-command leaves a marker. The case used to assert only the
# STOPPED state, which a plain SIGTERM reaches without any stop-command.

wait_for_service "stop-cmd-svc" "STARTED" 10

rm -f /run/26-stop-marker
assert_eq "$(cat /run/26-stop-marker 2>/dev/null)" "" "no marker while running"

slinitctl --system stop stop-cmd-svc 2>&1
wait_for_service "stop-cmd-svc" "STOPPED" 15

assert_service_state "stop-cmd-svc" "STOPPED" "service stopped"
assert_eq "$(cat /run/26-stop-marker 2>/dev/null)" "stopped-by-command" \
    "stop-command ran on stop"

slinitctl --system start stop-cmd-svc 2>&1
wait_for_service "stop-cmd-svc" "STARTED" 10

test_summary
