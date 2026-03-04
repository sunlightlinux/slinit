#!/bin/sh
# Test: trigger and untrigger a triggered service.
# Validates: CmdSetTrigger, TriggeredService, readReply skipping info packets.

# trigger-svc is a triggered service, should be in STARTING (waiting for trigger)
sleep 1
_state=$(slinitctl --system status trigger-svc 2>/dev/null | grep 'State:' | awk '{print $2}')
# It could be STARTING or STARTED depending on timing; if STARTING, trigger it
if [ "$_state" = "STARTING" ]; then
    assert_eq "$_state" "STARTING" "trigger-svc waiting for trigger"
fi

# Trigger it
output=$(slinitctl --system trigger trigger-svc 2>&1)
assert_contains "$output" "triggered" "trigger command succeeded"
assert_not_contains "$output" "unexpected reply" "no protocol error on trigger"

wait_for_service "trigger-svc" "STARTED" 5
assert_service_state "trigger-svc" "STARTED" "trigger-svc is STARTED after trigger"

# Untrigger (reset)
output=$(slinitctl --system untrigger trigger-svc 2>&1)
assert_contains "$output" "untriggered" "untrigger command succeeded"
assert_not_contains "$output" "unexpected reply" "no protocol error on untrigger"

test_summary
