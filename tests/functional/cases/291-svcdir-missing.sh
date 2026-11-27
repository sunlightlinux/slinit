#!/bin/sh
# Test: with the whole service directory gone (/etc/slinit.d removed
# by the .d/rootfs/sbin/init wrapper before slinit starts), PID 1
# survives, reports that no boot service file exists in the searched
# directories, and its control socket answers at the recovery menu.
# This script runs outside the service graph (there is none).

CATCH_ALL=/run/slinit/catch-all.log

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ ! -e /etc/slinit.d ]; then
    echo "OK: /etc/slinit.d is absent"
else
    echo "FAIL: /etc/slinit.d exists, the case set-up did not run"; _TESTS_FAILED=$((_TESTS_FAILED + 1))
fi
_TESTS_RUN=$((_TESTS_RUN + 1))
if kill -0 1 2>/dev/null && [ "$(cat /proc/1/comm)" = "slinit" ]; then
    echo "OK: PID 1 is slinit and alive"
else
    echo "FAIL: PID 1 is not a live slinit"; _TESTS_FAILED=$((_TESTS_FAILED + 1))
fi

_out=$(timeout 5 slinitctl --system list 2>&1); _rc=$?
assert_eq "$_rc" "0" "slinitctl list answers with no service directory"
# A load attempt while the directory is missing fails cleanly.
_out=$(timeout 10 slinitctl --system start anything 2>&1); _rc=$?
echo "start anything: rc=$_rc: $_out"
assert_contains "$_out" "not found" "starting a service reports it is not found"

sleep 1
_log=$(cat "$CATCH_ALL" 2>/dev/null)
echo "--- catch-all:"; echo "$_log" | tail -20; echo "---"
assert_contains "$_log" "Failed to load 'boot'" "the load failure is logged"
assert_contains "$_log" "rescue menu" "slinit says it is entering the recovery menu"

test_summary
