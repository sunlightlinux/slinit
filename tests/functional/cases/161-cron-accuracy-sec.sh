#!/bin/sh
# Test: cron-accuracy-sec parses cleanly and doesn't wedge the
# start path. Full bucket-coalescing verification requires
# multiple wall-clock waits + observation of drift — impractical
# in CI.
wait_for_service "cron-acc-svc" "STARTED" 10
assert_service_state "cron-acc-svc" "STARTED" "svc with cron-accuracy-sec reached STARTED"
test_summary
