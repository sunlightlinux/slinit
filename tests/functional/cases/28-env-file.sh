#!/bin/sh
# Test: env-file loads variables into service environment.
# Validates: env-file directive, variable availability in command.

wait_for_service "envfile-svc" "STARTED" 10

# Give it time to produce output
sleep 2

# Check that env-file variables are present in the service output
output=$(slinitctl --system catlog envfile-svc 2>&1)
assert_contains "$output" "TEST_KEY=test_value" "env-file variable loaded"
assert_contains "$output" "ANOTHER=123" "second env-file variable loaded"

test_summary
