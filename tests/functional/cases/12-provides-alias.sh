#!/bin/sh
# Test: service alias via 'provides' setting.
# Validates: provides lookup, FindService by alias.

wait_for_service "alias-svc" "STARTED" 10

# Should be findable by alias
output=$(slinitctl --system status my-alias 2>&1)
assert_contains "$output" "STARTED" "service found by alias 'my-alias'"

# is-started should work with alias
assert_exit_code "slinitctl --system is-started my-alias" 0 "is-started works with alias"

test_summary
