#!/bin/sh
# Test: 'down' marker file prevents service from auto-starting.
# Validates: down-file detection, markedDown flag, explicit start clears it.
#
# boot pulls down-svc in with waits-for, so without down-svc.down it
# would be running by now. (Nothing pulled it in before, so the case
# passed with or without the marker.)

wait_for_service "boot" "STARTED" 10
sleep 1

assert_service_state "down-svc" "STOPPED" "down file kept a pulled-in service stopped"

slinitctl --system start down-svc >/dev/null 2>&1
wait_for_service "down-svc" "STARTED" 5
assert_service_state "down-svc" "STARTED" "explicit start overrides down file"

slinitctl --system stop down-svc >/dev/null 2>&1
sleep 1
assert_service_state "down-svc" "STOPPED" "down-svc stopped cleanly"

test_summary
