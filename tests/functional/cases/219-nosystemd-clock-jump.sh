#!/bin/sh
# nosystemd checklist — systemd#1143.
#
# "PID1 getting stuck printing 'systemd[1]: Time has been changed'
# continuously": a wall-clock step made systemd's PID 1 spin, flooding
# the console and starving everything else.
# https://github.com/systemd/systemd/issues/1143
#
# slinit must survive a clock step: PID 1 stays responsive, supervised
# services keep running, and nothing loops on the event. Timers are
# armed off the monotonic clock precisely so a wall-clock jump is a
# non-event.

wait_for_service "clockprobe" "STARTED" 10

before_pid=$(slinitctl status clockprobe 2>/dev/null | awk '/PID:/{print $2}')

# Step the wall clock forward a year, then back. Both directions matter:
# the upstream bug fired on the backwards step.
date -s "+1 year" >/dev/null 2>&1
sleep 2
date -s "-1 year" >/dev/null 2>&1
sleep 2

# PID 1 must still answer the control socket. This is the assertion
# that actually fails if PID 1 is wedged in a loop.
out=$(slinitctl list 2>&1)
assert_contains "$out" "clockprobe" "PID 1 still answers after a clock step"

# The service must not have been restarted or killed by the jump.
after_pid=$(slinitctl status clockprobe 2>/dev/null | awk '/PID:/{print $2}')
assert_eq "$after_pid" "$before_pid" "clock step did not churn the service"

assert_service_state "clockprobe" "STARTED" "service survived the clock step"

# A spinning PID 1 shows up as sustained CPU. utime+stime in clock ticks
# from /proc/1/stat; a couple of seconds of spinning would be hundreds
# of ticks, normal supervision is single digits.
ticks=$(awk '{print $14 + $15}' /proc/1/stat 2>/dev/null)
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -n "$ticks" ] && [ "$ticks" -lt 200 ]; then
    echo "OK: PID 1 CPU time is $ticks ticks — not spinning"
else
    echo "FAIL: PID 1 burned $ticks ticks of CPU (systemd#1143 shape)"
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
fi

test_summary
