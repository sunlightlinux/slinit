#!/bin/sh
# Test: with no boot service file at all, PID 1 survives, says why on
# the console, and its control socket answers.
#
# The .d/rootfs/sbin/init wrapper removes /etc/slinit.d/boot before it
# execs slinit and runs this script outside the service graph (nothing
# in the graph can start). What slinit should do — as dinit does when
# its boot service cannot load — is report the load failure and park on
# the recovery menu rather than exit (exit as PID 1 is a kernel panic).
# While the menu is up the control socket stays up, so an operator with
# a way in (or this runner) can still query and shut down.

CATCH_ALL=/run/slinit/catch-all.log

_TESTS_RUN=$((_TESTS_RUN + 1))
if kill -0 1 2>/dev/null && [ "$(cat /proc/1/comm)" = "slinit" ]; then
    echo "OK: PID 1 is slinit and alive"
else
    echo "FAIL: PID 1 is not a live slinit"; _TESTS_FAILED=$((_TESTS_FAILED + 1))
fi

_out=$(timeout 5 slinitctl --system list 2>&1); _rc=$?
assert_eq "$_rc" "0" "slinitctl list answers while boot cannot load"
assert_eq "$(echo "$_out" | grep -c ' boot$')" "0" "boot is not listed as loaded"

sleep 1
_log=$(cat "$CATCH_ALL" 2>/dev/null)
echo "--- catch-all:"; echo "$_log" | tail -20; echo "---"
assert_contains "$_log" "Failed to load 'boot'" "the load failure is logged"
assert_contains "$_log" "rescue menu" "slinit says it is entering the recovery menu"

test_summary
