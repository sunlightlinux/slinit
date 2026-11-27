#!/bin/sh
# Test: `single` (sysvinit runlevel 1) on the kernel command line selects
# rescue mode: it skips the service graph
# and gives a shell on /dev/console, with the control socket up.
#
# slinit(8): slinit.rescue / slinit.emergency, and the bare words
# rescue / emergency / single / s / 1, select the mode; neither loads
# boot. .d/kernel-args adds `single`; the .d/rootfs/sbin/init
# wrapper runs this script outside the graph, which is not loaded.

CATCH_ALL=/run/slinit/catch-all.log
MODE=rescue

_out=$(timeout 5 slinitctl --system list 2>&1); _rc=$?
assert_eq "$_rc" "0" "slinitctl list answers in $MODE mode"
assert_not_contains "$_out" "boot" "boot is not loaded"
assert_not_contains "$_out" "test-runner" "the test-runner service is not started"

sleep 1
_log=$(cat "$CATCH_ALL" 2>/dev/null)
echo "--- catch-all:"; echo "$_log" | tail -15; echo "---"
# The console (and so the catch-all log) shows warnings and errors
# only. The line saying a shell is up, and that leaving it reboots, must
# get through: it used to be a NOTICE, and the operator saw only
# "slinit booting..." and a bare prompt.
assert_contains "$_log" "bootmode=$MODE: shell active on /dev/console" "the console says a $MODE shell is up and how to leave"

# The shell is a child of PID 1 holding the console.
_sh=$(ps -o pid,ppid,args 2>/dev/null | awk '$2 == 1 && /sh/ && !/guest-runner/ {print $1; exit}')
echo "shell under PID 1: ${_sh:-none}"
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -n "$_sh" ]; then
    echo "OK: a shell runs under PID 1 (pid $_sh)"
else
    echo "FAIL: no shell under PID 1"; _TESTS_FAILED=$((_TESTS_FAILED + 1))
fi
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$(cat /proc/1/comm)" = "slinit" ]; then
    echo "OK: PID 1 is slinit and alive"
else
    echo "FAIL: PID 1 is not slinit"; _TESTS_FAILED=$((_TESTS_FAILED + 1))
fi

test_summary
