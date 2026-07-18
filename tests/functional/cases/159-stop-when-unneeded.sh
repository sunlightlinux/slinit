#!/bin/sh
# Test: stop-when-unneeded auto-stops a helper after its last
# dependent falls away. swu-dependent depends-on swu-helper, so
# helper starts. When we stop the dependent, helper should follow.
wait_for_service "swu-dependent" "STARTED" 10
wait_for_service "swu-helper" "STARTED" 10
assert_service_state "swu-helper" "STARTED" "helper up while dependent runs"

slinitctl --system stop swu-dependent >/dev/null 2>&1
wait_for_service "swu-dependent" "STOPPED" 10

# Give the propagation queue a beat to fire the auto-stop.
_e=0
while [ "$_e" -lt 8 ]; do
    [ "$(svc_state swu-helper)" = "STOPPED" ] && break
    sleep 1; _e=$((_e + 1))
done
assert_service_state "swu-helper" "STOPPED" "helper auto-stopped when unneeded"

test_summary
