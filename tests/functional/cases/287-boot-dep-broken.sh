#!/bin/sh
# Test: a boot service whose hard dependency does not parse (and whose
# soft dependency does not exist) fails to load with an error that
# names the dependency; PID 1 survives at the recovery menu and its
# control socket answers, including a start of an unrelated service.
#
# The .d/rootfs/sbin/init wrapper writes boot -> depends-on broken-dep
# (an invalid `restart` value) + waits-for absent-dep, and runs this
# script outside the service graph. dinit fails the whole load when a
# dependency fails to load, waits-for included.

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
assert_contains "$_log" "broken-dep" "the error names the broken dependency"
assert_contains "$_log" "rescue menu" "slinit says it is entering the recovery menu"

# No event loop runs while the menu is up. A start sent over the socket
# must still come back, one way or the other, rather than hang the
# client (slinitctl start waits for the outcome).
_t0=$(cut -d. -f1 /proc/uptime)
_out=$(timeout 15 slinitctl --system start idle-svc 2>&1); _rc=$?
_t1=$(cut -d. -f1 /proc/uptime)
echo "start idle-svc: rc=$_rc in $((_t1 - _t0))s: $_out"
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$_rc" -ne 124 ]; then
    echo "OK: a start during the recovery menu returns (rc=$_rc)"
else
    echo "FAIL: a start during the recovery menu hung the client"
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
fi
_out=$(timeout 5 slinitctl --system list 2>&1); _rc=$?
assert_eq "$_rc" "0" "slinitctl list still answers after the start"

test_summary
