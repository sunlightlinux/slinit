#!/bin/sh
# Test: env-dir loads environment variables (one file per variable, runit-style).
# Validates: env-dir directive, ReadEnvDir, variable availability in command.

wait_for_service "envdir-svc" "STARTED" 10

# Give time for output
sleep 2

output=$(slinitctl --system catlog envdir-svc 2>&1)
assert_contains "$output" "MY_HOST=localhost" "env-dir variable MY_HOST loaded"
assert_contains "$output" "MY_PORT=8080" "env-dir variable MY_PORT loaded"
assert_contains "$output" "MY_APP=slinit-test" "env-dir variable MY_APP loaded"

test_summary
