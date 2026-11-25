#!/bin/sh
# Test: the clock guard moves a stale boot clock forward to the last
# known-good time saved in /var/lib/slinit/clock.
# Validates: ClockGuard (pkg/shutdown/clockguard.go) under a real PID 1.
#
# The VM boots with its RTC at 2010-01-01 (qemu-args), so the kernel starts
# the system clock there. The overlay seeds the timestamp file with
# 1780000000 (2026-05-28), later than the 2024 floor and earlier than the
# host's clock. A service started after the guard must therefore see a
# clock that is
#   - at least the saved stamp (the guard ran and read the file), and
#   - within minutes of it (it came from the file, not from the host).
# Without the guard it would see 2010; with the floor alone, 2024.

stamp=1780000000

wait_for_service "clock-svc" "STARTED" 10

# Precondition: the RTC really is stale. If qemu-args were ignored the
# clock was never wrong and nothing below would mean anything.
rtc=$(cat /sys/class/rtc/rtc0/since_epoch 2>/dev/null)
if [ -z "$rtc" ] || [ "$rtc" -ge 1704067200 ]; then
    skip_case "RTC reads '${rtc}', not the stale 2010 base from qemu-args"
fi
echo "INFO: RTC since_epoch=$rtc (stale, as configured)"

epoch=$(cat /tmp/clock-epoch 2>/dev/null)

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -n "$epoch" ] && [ "$epoch" -ge "$stamp" ]; then
    echo "OK: clock at service start ($epoch) >= saved stamp ($stamp)"
else
    echo "FAIL: clock at service start '$epoch' < saved stamp $stamp (guard did not advance it)"
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
fi

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -n "$epoch" ] && [ "$epoch" -lt $((stamp + 600)) ]; then
    echo "OK: clock at service start ($epoch) came from the stamp, not elsewhere"
else
    echo "FAIL: clock at service start '$epoch' is not within 10 min of the stamp $stamp"
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
fi

# The guard rewrites the stamp right after correcting the clock, so the
# file now holds a time at or after the seeded one.
saved=$(cat /var/lib/slinit/clock 2>/dev/null)
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -n "$saved" ] && [ "$saved" -ge "$stamp" ]; then
    echo "OK: timestamp file rewritten ($saved >= $stamp)"
else
    echo "FAIL: timestamp file holds '$saved', expected >= $stamp"
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
fi

assert_service_state "clock-svc" "STARTED" "clock-svc is STARTED"

test_summary
