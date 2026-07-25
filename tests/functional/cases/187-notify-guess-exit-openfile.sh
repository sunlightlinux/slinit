#!/bin/sh
# Test: notify-access + guess-main-pid + exit-type + open-file all
# parse and coexist on one service. Behavioural verification of each
# needs elaborate fixtures (real sd_notify sender, cgroup.procs walk,
# etc.) that don't add regression value beyond what pkg unit tests
# already cover — the loader smoke is the check we want here.

wait_for_service "notify-svc" "STARTED" 10
assert_service_state "notify-svc" "STARTED" "notify+guess+exit-type+open-file cluster parses"

test_summary
