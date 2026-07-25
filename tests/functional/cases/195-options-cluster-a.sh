#!/bin/sh
# Test: options-flags cluster A — unmask-intr / skippable /
# start-interruptible round-trip through the parser and boot graph.
# Each of these is a Flags struct bit that the parser sets; a
# regression that dropped one would leave the service either failing
# to parse or running with unintended behaviour.

wait_for_service "optsa-svc" "STARTED" 10
assert_service_state "optsa-svc" "STARTED" "options=unmask-intr,skippable,start-interruptible parses"

test_summary
