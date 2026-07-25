#!/bin/sh
# Test: cpu-pressure-watch/threshold + io-pressure-watch/threshold.
# Validates: parse + start for both PSI watches on the cpu and io
# controllers (memory PSI is covered by 141). A watcher that failed
# to open its pressure file would log an error but the service still
# reaches STARTED — that's the fail-open contract for PSI.
#
# Semantics under test: pkg/service/record.go PSI watch install path
# + cgroup v2 pressure interface polling.

wait_for_service "cpupsi-svc" "STARTED" 10
wait_for_service "iopsi-svc" "STARTED" 10
assert_service_state "cpupsi-svc" "STARTED" "cpu-pressure-watch/threshold parses + service starts"
assert_service_state "iopsi-svc" "STARTED" "io-pressure-watch/threshold parses + service starts"

test_summary
