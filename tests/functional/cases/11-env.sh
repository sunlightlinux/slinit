#!/bin/sh
# Test: runtime environment variable management.
# Validates: CmdSetEnv, CmdGetAllEnv, per-service and global env.

wait_for_service "env-svc" "STARTED" 10

# Set a per-service env var
slinitctl --system setenv env-svc MY_VAR=hello123

# Read it back
output=$(slinitctl --system getallenv env-svc 2>&1)
assert_contains "$output" "MY_VAR=hello123" "per-service env var set"

# Unset it
slinitctl --system unsetenv env-svc MY_VAR
output=$(slinitctl --system getallenv env-svc 2>&1)
assert_not_contains "$output" "MY_VAR" "per-service env var unset"

# Global env
slinitctl --system setenv-global GLOBAL_KEY=world456
output=$(slinitctl --system getallenv-global 2>&1)
assert_contains "$output" "GLOBAL_KEY=world456" "global env var set"

slinitctl --system unsetenv-global GLOBAL_KEY
output=$(slinitctl --system getallenv-global 2>&1)
assert_not_contains "$output" "GLOBAL_KEY" "global env var unset"

test_summary
