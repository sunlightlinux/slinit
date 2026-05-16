#!/bin/sh
# Test: upstart-style <service>.override drop-in files.
# Validates: a sibling override-svc.override in the same directory as
# override-svc is parsed after the base file and replaces its scalars
# (command + description) without the base file being edited.

wait_for_service "override-svc" "STARTED" 10

sleep 2

# The override replaced the command; the base would have written "base".
result=$(cat /tmp/override-result 2>/dev/null)
assert_eq "$result" "overridden" "override file replaced the command"

# The override also replaced the description (visible via status).
status=$(slinitctl --system status override-svc 2>&1)
assert_contains "$status" "overridden config" "description from .override file"

assert_service_state "override-svc" "STARTED" "override-svc is STARTED"

test_summary
