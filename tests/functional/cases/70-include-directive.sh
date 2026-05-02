#!/bin/sh
# Test: @include directive inlines another file into the service definition.
# Validates: @include parsing, settings from included file applied.

wait_for_service "include-svc" "STARTED" 10

sleep 2

# Verify the command from the included file ran
result=$(cat /tmp/include-result 2>/dev/null)
assert_eq "$result" "included" "@include file provided the command"

# Verify description from included file via status command
status=$(slinitctl --system status include-svc 2>&1)
assert_contains "$status" "included config" "description from @include file"

assert_service_state "include-svc" "STARTED" "include-svc is STARTED"

test_summary
