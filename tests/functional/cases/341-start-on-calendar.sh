#!/bin/sh
# Test: start-on-calendar under PID 1 (4.1).
#
# A oneshot job with start-on-calendar every second and manual = yes is
# armed when it loads and run by the calendar alone, again and again:
# loading it starts nothing, and a few seconds later it has run more
# than once.

cat > /etc/slinit.d/test-341 <<'SVC'
type = scripted
command = /bin/sh -c "echo x >> /run/test-341.runs"
start-on-calendar = *-*-* *:*:*
manual = yes
SVC
rm -f /run/test-341.runs
# Loading it arms the calendar; nothing else starts it.
slinitctl --system status test-341 >/dev/null 2>&1
sleep 4
n=$(wc -l < /run/test-341.runs 2>/dev/null || echo 0)
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "${n:-0}" -ge 2 ]; then
    echo "OK: the calendar ran the job $n times in 4s"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: the job ran ${n:-0} times in 4s, want at least 2"
fi
sed -i '/start-on-calendar/d' /etc/slinit.d/test-341
slinitctl --system reload test-341 >/dev/null 2>&1
sleep 1
m=$(wc -l < /run/test-341.runs 2>/dev/null || echo 0)
sleep 2
m2=$(wc -l < /run/test-341.runs 2>/dev/null || echo 0)
assert_eq "$m2" "$m" "a reload that drops start-on-calendar disarms it"

# A systemd unit's .timer is read with it: OnCalendar= is its
# start-on-calendar, with manual = yes.
rm -f /run/test-341-unit.runs
slinitctl --system status test-341-unit >/dev/null 2>&1
sleep 4
n=$(wc -l < /run/test-341-unit.runs 2>/dev/null || echo 0)
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "${n:-0}" -ge 2 ]; then
    echo "OK: the unit's timer ran it $n times in 4s"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: the unit ran ${n:-0} times in 4s from its .timer, want at least 2"
fi

# The monotonic timers: start-on-boot-sec past already runs once at
# load; start-on-unit-active-sec reruns a second after each start.
cat > /etc/slinit.d/test-341-boot <<'SVC'
type = scripted
command = /bin/sh -c "echo x >> /run/test-341-boot.runs"
start-on-boot-sec = 1s
manual = yes
SVC
cat > /etc/slinit.d/test-341-every <<'SVC'
type = scripted
command = /bin/sh -c "echo x >> /run/test-341-every.runs"
start-on-unit-active-sec = 1s
SVC
rm -f /run/test-341-boot.runs /run/test-341-every.runs
slinitctl --system status test-341-boot >/dev/null 2>&1
slinitctl --system start test-341-every >/dev/null 2>&1
sleep 3
assert_eq "$(wc -l < /run/test-341-boot.runs 2>/dev/null)" "1" "start-on-boot-sec (past) ran once"
n=$(wc -l < /run/test-341-every.runs 2>/dev/null || echo 0)
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "${n:-0}" -ge 2 ]; then
    echo "OK: start-on-unit-active-sec ran the job $n times in 3s"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: start-on-unit-active-sec ran it ${n:-0} times in 3s, want at least 2"
fi
slinitctl --system stop test-341-every >/dev/null 2>&1

# list-timers shows the loaded calendar services, by next run.
lt=$(slinitctl --system list-timers 2>&1)
assert_contains "$lt" "test-341-unit" "list-timers lists the timer's service"
assert_contains "$lt" "NEXT" "list-timers prints its header"

test_summary
