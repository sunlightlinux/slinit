#!/bin/sh
# 256-unload-held-by-monitor: unload is refused while another client
# (slinit-monitor here) holds the service, as dinit does. It used to unload
# it, and the monitor's stale handle then drove whatever service was loaded
# under that name next.

slinitctl --system start heldsvc > /dev/null 2>&1
wait_for_service "heldsvc" "STARTED" 5
slinitctl --system stop heldsvc > /dev/null 2>&1
wait_for_service "heldsvc" "STOPPED" 5

slinit-monitor --system -c "true" heldsvc > /dev/null 2>&1 &
mon=$!
sleep 1
out=$(slinitctl --system unload heldsvc 2>&1)
assert_not_contains "$out" "unloaded" "unload refused while slinit-monitor holds the service"
assert_service_state "heldsvc" "STOPPED" "service still loaded"

kill "$mon" 2>/dev/null; wait "$mon" 2>/dev/null
sleep 1
out=$(slinitctl --system unload heldsvc 2>&1)
assert_contains "$out" "unloaded" "unload succeeds once the monitor is gone"

test_summary
