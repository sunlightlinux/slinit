#!/bin/sh
# Test: the shutdown command reaches the daemon over the control socket.
# Validates: schedule, query and cancel of a shutdown.
#
# An immediate shutdown would power the VM off before guest-runner
# writes results, so the case schedules one ten minutes out, checks the
# daemon reports it pending, and cancels it. (It used to pass an
# invalid type and look for "unknown shutdown type", which slinitctl
# prints itself before it ever connects.)

wait_for_service "boot" "STARTED" 10

out=$(slinitctl --system shutdown --status 2>&1)
assert_contains "$out" "No shutdown is scheduled" "nothing pending at start"

slinitctl --system shutdown poweroff +10 >/dev/null 2>&1
out=$(slinitctl --system shutdown --status 2>&1)
echo "INFO: after scheduling: $out"
assert_contains "$out" "Shutdown (poweroff) scheduled" "daemon reports the scheduled poweroff"

slinitctl --system shutdown -c >/dev/null 2>&1
out=$(slinitctl --system shutdown --status 2>&1)
assert_contains "$out" "No shutdown is scheduled" "cancel cleared it"

assert_service_state "boot" "STARTED" "boot still STARTED"

test_summary
