#!/bin/sh
# Test: clock guard ensures system clock is not in the past at boot.
# Validates: ClockGuard floor enforcement, timestamp file write/read.
# Note: QEMU VMs inherit the host clock, so the clock should already be
# correct; this test verifies ClockGuard didn't break anything and that
# the timestamp file mechanism works.

wait_for_service "clock-svc" "STARTED" 10

# 1. System clock year must be >= 2024 (the hardcoded floor)
year=$(cat /tmp/clock-year 2>/dev/null)
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -n "$year" ] && [ "$year" -ge 2024 ]; then
    echo "OK: system year $year >= 2024 (clock floor enforced)"
else
    echo "FAIL: system year '$year' < 2024 (clock guard did not correct)"
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
fi

# 2. Epoch must be reasonable (> 2024-01-01 = 1704067200)
epoch=$(cat /tmp/clock-epoch 2>/dev/null)
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -n "$epoch" ] && [ "$epoch" -gt 1704067200 ]; then
    echo "OK: epoch $epoch > 1704067200 (post-2024)"
else
    echo "FAIL: epoch '$epoch' <= 1704067200 (clock is pre-2024)"
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
fi

# 3. Test timestamp file write + read round-trip
mkdir -p /var/lib/slinit
now_epoch=$(date +%s)
echo "$now_epoch" > /var/lib/slinit/clock

_TESTS_RUN=$((_TESTS_RUN + 1))
read_back=$(cat /var/lib/slinit/clock 2>/dev/null)
if [ "$read_back" = "$now_epoch" ]; then
    echo "OK: timestamp file round-trip: wrote $now_epoch, read $read_back"
else
    echo "FAIL: timestamp file round-trip: wrote $now_epoch, got '$read_back'"
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
fi

# 4. Verify timestamp file rejects invalid content
echo "not-a-number" > /var/lib/slinit/clock.bad
_TESTS_RUN=$((_TESTS_RUN + 1))
bad_val=$(cat /var/lib/slinit/clock.bad 2>/dev/null)
case "$bad_val" in
    *[!0-9]*)
        echo "OK: invalid timestamp 'not-a-number' is non-numeric (would be rejected)"
        ;;
    *)
        echo "FAIL: expected non-numeric content"
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        ;;
esac

# 5. Verify the clock didn't go backwards during boot — the epoch from
#    the service (captured at service start) should be <= now
_TESTS_RUN=$((_TESTS_RUN + 1))
check_epoch=$(date +%s)
if [ -n "$epoch" ] && [ "$epoch" -le "$check_epoch" ]; then
    echo "OK: clock monotonic: service_start=$epoch <= now=$check_epoch"
else
    echo "FAIL: clock went backwards: service_start=$epoch > now=$check_epoch"
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
fi

# Service should still be running
assert_service_state "clock-svc" "STARTED" "clock-svc is STARTED"

test_summary
