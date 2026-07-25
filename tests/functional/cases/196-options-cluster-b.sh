#!/bin/sh
# Test: options = runs-on-console (console-attached STARTED state).
# starts-on-console needs a two-service arbiter that fits acceptance
# tests better; the runs-on-console smoke is enough to prove the flag
# parses and doesn't break the boot path on a bare fixture.

wait_for_service "optsb-svc" "STARTED" 10
assert_service_state "optsb-svc" "STARTED" "options=runs-on-console parses + service starts"

test_summary
