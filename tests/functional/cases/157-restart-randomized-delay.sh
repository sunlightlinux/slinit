#!/bin/sh
# Test: restart-randomized-delay adds a random 0..N jitter to each
# restart delay.
#
# The service fails at once and restarts with restart-delay = 0.1 s and
# restart-randomized-delay = 0.4 s; each attempt stamps /proc/uptime.
# Every gap must lie in [0.1 s, 0.5 s + slack], and they must not all be
# the same: without the jitter they are all 0.1 s. (The case used to run
# a service with restart = no and check it started.)

sleep 4

n=$(wc -l < /tmp/157-times 2>/dev/null || echo 0)
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$n" -ge 6 ]; then
    echo "OK: $n attempts recorded"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: only $n attempts recorded, need 6"
    test_summary
fi

# gaps in ms, then min, max
set -- $(awk 'NR > 1 { printf "%d\n", ($1 - p) * 1000 } { p = $1 }' /tmp/157-times | sort -n | awk 'NR == 1 { mn = $1 } { mx = $1 } END { print mn, mx }')
min=$1 max=$2
echo "INFO: gaps between $min and $max ms"

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$min" -ge 90 ] && [ "$max" -le 650 ]; then
    echo "OK: every gap within 0.1 s + [0, 0.4 s] (and slack)"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: gaps $min..$max ms outside 90..650"
fi

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$((max - min))" -ge 80 ]; then
    echo "OK: gaps vary by $((max - min)) ms: the delay is randomized"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: gaps vary by only $((max - min)) ms: no jitter applied"
fi

test_summary
