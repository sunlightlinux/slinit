#!/bin/sh
# Test: wake (start without marking active) and release (remove active mark).
# Validates: CmdWakeService, CmdReleaseService.

# wake-svc is loaded but not started (no dependency pulls it)
sleep 1

# Start it with 'start' (marks active)
slinitctl --system start wake-svc
wait_for_service "wake-svc" "STARTED" 10
assert_service_state "wake-svc" "STARTED" "wake-svc started"

# Release removes the active mark; since nothing else depends on it, it stops
slinitctl --system release wake-svc
wait_for_service "wake-svc" "STOPPED" 10
assert_service_state "wake-svc" "STOPPED" "wake-svc stopped after release"

# Wake without an active dependent should NAK (no one needs it)
output=$(slinitctl --system wake wake-svc 2>&1)
assert_contains "$output" "no active dependents" "wake response received"

test_summary
