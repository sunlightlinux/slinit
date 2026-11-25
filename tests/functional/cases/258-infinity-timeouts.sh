#!/bin/sh
# 258-infinity-timeouts: a timeout of `infinity` (what converted systemd units
# and hand-written configs use) loads and means "no limit". The overflow fix
# first refused it; it now saturates.

slinitctl --system start infsvc > /dev/null 2>&1
wait_for_service "infsvc" "STARTED" 5
assert_service_state "infsvc" "STARTED" "service with infinity timeouts loads and starts"
slinitctl --system stop infsvc > /dev/null 2>&1
wait_for_service "infsvc" "STOPPED" 5
assert_service_state "infsvc" "STOPPED" "and stops"

test_summary
