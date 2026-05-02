#!/bin/sh
# Test: restart backoff with progressive delay.
# Validates: restart-delay-step, restart-delay-cap, increasing delay between restarts.

wait_for_service "backoff-svc" "STARTED" 10 || true

# Wait for several restart cycles:
# restart 1: delay=1s, restart 2: delay=2s, restart 3: delay=3s (cap), restart 4: delay=3s
sleep 15

# Read timestamps of each restart attempt
lines=$(wc -l < /tmp/backoff-times 2>/dev/null || echo 0)
assert_eq "$([ "$lines" -ge 3 ] && echo yes || echo no)" "yes" \
    "at least 3 restart attempts recorded (got $lines)"

# Verify delays are increasing: gap between attempt 1->2 should be < gap 2->3
if [ "$lines" -ge 3 ]; then
    t1=$(sed -n '1p' /tmp/backoff-times)
    t2=$(sed -n '2p' /tmp/backoff-times)
    t3=$(sed -n '3p' /tmp/backoff-times)

    gap1=$((t2 - t1))
    gap2=$((t3 - t2))

    _TESTS_RUN=$((_TESTS_RUN + 1))
    if [ "$gap2" -ge "$gap1" ]; then
        echo "OK: delay increased: gap1=${gap1}s, gap2=${gap2}s"
    else
        echo "FAIL: delay did not increase: gap1=${gap1}s, gap2=${gap2}s"
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
    fi

    # Verify cap: gap should not exceed cap (3s) + tolerance (2s for scheduling)
    if [ "$lines" -ge 4 ]; then
        t4=$(sed -n '4p' /tmp/backoff-times)
        gap3=$((t4 - t3))
        _TESTS_RUN=$((_TESTS_RUN + 1))
        if [ "$gap3" -le 6 ]; then
            echo "OK: delay capped: gap3=${gap3}s <= 6s (cap=3 + tolerance)"
        else
            echo "FAIL: delay exceeded cap: gap3=${gap3}s > 6s"
            _TESTS_FAILED=$((_TESTS_FAILED + 1))
        fi
    fi
fi

test_summary
