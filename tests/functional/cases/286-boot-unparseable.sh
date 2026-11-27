#!/bin/sh
# Test: a boot service file with a syntax error does not take PID 1
# down; the console names the file and the line, and the control
# socket answers while slinit waits at the recovery menu.
#
# The .d/rootfs/sbin/init wrapper writes /etc/slinit.d/boot with a
# setting-less third line and runs this script outside the service
# graph (nothing in it can start). dinit reports a load error with the
# file and line in the same situation.

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
assert_contains "$_log" "/etc/slinit.d/boot:3" "the error names the file and line"
assert_contains "$_log" "rescue menu" "slinit says it is entering the recovery menu"
# The file is there; what is wrong is its content. A message saying
# no service file was found sends the operator looking in the wrong
# place.
assert_not_contains "$_log" "not found" "the error does not claim the file is missing"

test_summary
