#!/bin/sh
# Test: restart backoff with progressive delay.
# Validates: restart-delay-step, restart-delay-cap, increasing delay between restarts.
#
# Gaps are measured from /proc/uptime in hundredths of a second. With
# whole-second `date +%s` stamps and "gap2 >= gap1", a constant delay
# (no step) and an uncapped one both passed.

wait_for_service "backoff-svc" "STARTED" 10 || true
sleep 8

lines=$(wc -l < /tmp/backoff-times 2>/dev/null || echo 0)
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$lines" -ge 6 ]; then
    echo "OK: $lines attempts recorded"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: only $lines attempts recorded, need 6"
    test_summary
fi

# gap N in milliseconds (gap 1 = attempt 2 - attempt 1).
gap() { awk -v n="$1" 'NR == n { a = $1 } NR == n + 1 { printf "%d", ($1 - a) * 1000 }' /tmp/backoff-times; }
g1=$(gap 1); g2=$(gap 2); g3=$(gap 3); g4=$(gap 4); g5=$(gap 5)
echo "INFO: gaps (ms): $g1 $g2 $g3 $g4 $g5 — expected about 300 900 1500 1500 1500"

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$((g2 - g1))" -ge 400 ] && [ "$((g3 - g2))" -ge 400 ]; then
    echo "OK: each delay grew by about the step (0.6s)"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: delays did not grow by the step: $g1 $g2 $g3"
fi

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$g4" -ge 1200 ] && [ "$g4" -le 1800 ] && [ "$g5" -le 1800 ]; then
    echo "OK: delay held at the 1.5s cap ($g4, $g5)"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: delay not held at the 1.5s cap: $g4 $g5"
fi

test_summary
