#!/bin/sh
# Test: a dependency cycle reachable from boot (cyc-a -> cyc-b ->
# waits-for cyc-c -> cyc-a) is refused at load as a cycle; PID 1 does
# not recurse itself to death or hang, and its control socket answers
# at the recovery menu.
#
# The .d/rootfs/sbin/init wrapper writes the cycle and runs this script
# outside the service graph. dinit refuses a cycle at load ("has a
# dependency cycle"), soft edges included.

CATCH_ALL=/run/slinit/catch-all.log

_TESTS_RUN=$((_TESTS_RUN + 1))
if kill -0 1 2>/dev/null && [ "$(cat /proc/1/comm)" = "slinit" ]; then
    echo "OK: PID 1 is slinit and alive"
else
    echo "FAIL: PID 1 is not a live slinit"; _TESTS_FAILED=$((_TESTS_FAILED + 1))
fi

_out=$(timeout 5 slinitctl --system list 2>&1); _rc=$?
assert_eq "$_rc" "0" "slinitctl list answers while boot cannot load"

sleep 1
_log=$(cat "$CATCH_ALL" 2>/dev/null)
echo "--- catch-all:"; echo "$_log" | tail -20; echo "---"
assert_contains "$_log" "Failed to load 'boot'" "the load failure is logged"
assert_contains "$_log" "cyclic dependency" "the error says it is a dependency cycle"
assert_contains "$_log" "rescue menu" "slinit says it is entering the recovery menu"

test_summary
