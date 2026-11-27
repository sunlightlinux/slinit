#!/bin/sh
# Test: a boot-critical service that never becomes ready, with no start
# timeout, leaves boot STARTING for good — and PID 1 stays usable: the
# control socket answers (list, status, boot-time), and Ctrl-Alt-Del
# (SIGINT to PID 1) still starts the shutdown, which stops the hung
# service instead of waiting for its start.
#
# boot depends-on hang-proc (a process whose readiness pipe is never
# written) and waits-for hang-script (a scripted start that never
# exits). The .d/rootfs/sbin/init wrapper runs this script outside the
# service graph, so the shutdown it triggers does not stop the runner.

_state() { slinitctl --system status "$1" 2>/dev/null | awk '/State:/ {print $2; exit}'; }

sleep 2
assert_eq "$(_state boot)" "STARTING" "boot is still STARTING"
assert_eq "$(_state hang-proc)" "STARTING" "the hung dependency is STARTING"

_out=$(timeout 5 slinitctl --system list 2>&1); _rc=$?
assert_eq "$_rc" "0" "slinitctl list answers while boot hangs"
assert_contains "$_out" "hang-proc" "list shows the hung service"
_out=$(timeout 5 slinitctl --system boot-time 2>&1); _rc=$?
echo "boot-time: rc=$_rc: $_out"
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$_rc" -ne 124 ]; then
    echo "OK: boot-time returns while boot is unfinished (rc=$_rc)"
else
    echo "FAIL: boot-time hung while boot is unfinished"; _TESTS_FAILED=$((_TESTS_FAILED + 1))
fi

_pid=$(pgrep -f 'sleep 100000$')
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -n "$_pid" ]; then
    echo "OK: hang-proc's process is running (pid $_pid)"
else
    echo "FAIL: hang-proc's process not found"; _TESTS_FAILED=$((_TESTS_FAILED + 1))
fi

echo "CAD-PROBE: sending SIGINT to PID 1 (Ctrl-Alt-Del)"
kill -INT 1
_i=0
while [ "$_i" -lt 20 ] && [ -n "$_pid" ] && kill -0 "$_pid" 2>/dev/null; do
    sleep 0.5; _i=$((_i + 1))
done
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -n "$_pid" ] && ! kill -0 "$_pid" 2>/dev/null; then
    echo "OK: Ctrl-Alt-Del stopped the hung boot dependency within $((_i / 2))s"
else
    echo "FAIL: the hung boot dependency still runs 10s after Ctrl-Alt-Del"
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
fi
echo "info: hang-script process: $(pgrep -f 'sleep 100001$' || echo gone)"
echo "info: boot state now: $(_state boot)"
grep -a "Shutting down\|hang" /run/slinit/catch-all.log | tail -5

test_summary
