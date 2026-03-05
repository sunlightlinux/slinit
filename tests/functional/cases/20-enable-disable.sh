#!/bin/sh
# Test: enable and disable a service (adds/removes waits-for on boot + starts/stops).
# Validates: CmdEnableService, CmdDisableService.

sleep 1

# en-svc is not started (boot doesn't depend on it)
assert_exit_code "slinitctl --system is-started en-svc" 1 "en-svc not started initially"

# Enable it (adds waits-for to boot and starts it)
output=$(slinitctl --system enable en-svc 2>&1)
assert_contains "$output" "enabled" "enable command succeeded"

wait_for_service "en-svc" "STARTED" 10
assert_service_state "en-svc" "STARTED" "en-svc STARTED after enable"

# Disable it (removes waits-for from boot and stops it)
output=$(slinitctl --system disable en-svc 2>&1)
assert_contains "$output" "disabled" "disable command succeeded"

wait_for_service "en-svc" "STOPPED" 10
assert_service_state "en-svc" "STOPPED" "en-svc STOPPED after disable"

test_summary
