#!/bin/sh
# Test: timeout cluster (timeout-sec / timeout-abort-sec /
# timeout-start-failure-mode / timeout-stop-failure-mode) parses and
# coexists. Individual behaviour is covered by the failure paths in
# other tests (start-timeout in 170, stop-timeout escalation via
# stop-timeout-demo). Here we verify the parser + loader accept the
# stacked set without dropping any directive.

wait_for_service "tocluster-svc" "STARTED" 10
assert_service_state "tocluster-svc" "STARTED" "timeout cluster parses + service starts"

test_summary
