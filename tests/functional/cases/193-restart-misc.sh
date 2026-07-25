#!/bin/sh
# Test: restart-policy tail — start-limit-action, restart-force-exit-status,
# restart-max-delay, restart-mode, runtime-randomized-extra all parse and
# coexist. Behavioural verification of each individual knob would need
# controlled restart loops (slow, flaky); the loader smoke prevents a
# regression where any of these get dropped.

wait_for_service "restart-misc-svc" "STARTED" 10
assert_service_state "restart-misc-svc" "STARTED" "restart-policy tail parses + service starts"

test_summary
