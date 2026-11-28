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

test_summary
