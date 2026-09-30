#!/bin/sh
# Test: cron-persistent catch-up, and the interval-mode modifiers that
# used to be dropped on the floor.
# Validates: the on-disk last-run store is read at startup and drives a
# catch-up run; a current record does not; jitter and fixed-jitter do not
# break the interval loop.

# cron-overdue's record is from 2020 against a one-hour period, so the
# catch-up must fire promptly — well before the 45-minute cron-delay and
# long before the first real period.
i=0
while [ $i -lt 100 ]; do
    [ -f /run/cron-overdue.log ] && break
    sleep 0.1
    i=$((i + 1))
done

if [ -f /run/cron-overdue.log ]; then
    pass_count=$(grep -c caught-up /run/cron-overdue.log)
    assert_eq "$pass_count" "1" "overdue timer caught up exactly once"
else
    assert_eq "missing" "present" "overdue timer produced a catch-up run"
fi

# The record must be rewritten, or every later boot would catch up again
# for ever. It has to no longer be the seeded 2020 value.
stored=$(cat /var/lib/slinit/cron/cron-overdue 2>/dev/null)
assert_not_contains "$stored" "2020-01-01" "last-run record advanced past the seeded value"
assert_contains "$stored" "T" "last-run record is a timestamp ($stored)"

# The fresh timer must NOT have run: its record says it just fired and the
# period is an hour. Without this assertion a catch-up that always fires
# would look correct.
if [ -f /run/cron-fresh.log ]; then
    assert_eq "ran" "did-not-run" "current record must not trigger a catch-up"
else
    assert_eq "did-not-run" "did-not-run" "current record does not trigger a catch-up"
fi

# The jittered one-second ticker must still be firing. Before 2.4.8 the
# interval loop was a bare ticker and jitter never reached it; the rewrite
# waits the offset out after each tick, so this also checks the cadence
# did not stall.
i=0
while [ $i -lt 100 ]; do
    [ -f /run/cron-ticker.log ] && [ "$(grep -c tick /run/cron-ticker.log)" -ge 2 ] && break
    sleep 0.1
    i=$((i + 1))
done
ticks=$(grep -c tick /run/cron-ticker.log 2>/dev/null || echo 0)
if [ "$ticks" -ge 2 ]; then
    assert_eq "ok" "ok" "jittered interval timer fired repeatedly ($ticks times)"
else
    assert_eq "$ticks" ">=2" "jittered interval timer fired repeatedly"
fi

# A service with cron-persistent must have a record; one without must not
# have had a file invented for it.
assert_eq "$([ -f /var/lib/slinit/cron/cron-ticker ] && echo yes || echo no)" "no" \
    "a non-persistent timer writes no record"

test_summary
