#!/bin/sh
# Test: upstart-style "script ... end script" inline shell sugar.
# Validates: a script block becomes the service command via /bin/sh -c;
# the multi-line body runs (writes a marker) and the exec keeps the
# process alive so the type=process service reaches STARTED.

wait_for_service "script-svc" "STARTED" 10

sleep 2

result=$(cat /tmp/script-marker 2>/dev/null)
assert_eq "$result" "scripted" "script block body executed"

assert_service_state "script-svc" "STARTED" "script-svc is STARTED"

test_summary
