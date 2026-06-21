#!/bin/sh
# 42-cron-calendar — `cron-calendar = EXPR` schedules a sub-task using
# a systemd OnCalendar-style expression. The supported subset is in
# pkg/service/calendar.go (weekday lists, *-MM-DD date head, HH:MM[:SS]
# time field with '*' and 'N/STEP' patterns).
#
# Probe: `*-*-* *:*:*/5` fires at seconds 0,5,10,15,...,55 — at least
# 2 hits in a 12-second window.

SVC="acceptance-test-cron-cal"
MARK="/run/acceptance-test-cron-cal.log"

cleanup() {
    svc_remove "$SVC"
    rm -f "$MARK"
}
trap cleanup EXIT INT TERM

rm -f "$MARK"
: > "$MARK"

# Use the date-prefixed form. Parser splits on whitespace: first field
# is the date head, second is the time field. Without the date head the
# parser would try to read the whole thing as one time field which
# splits on ':' and yields too many components.
svc_deploy "$SVC" <<EOF
type = process
command = /bin/sh -c 'while :; do sleep 60; done'
cron-command = /bin/sh -c 'date +%s >> $MARK'
cron-calendar = *-*-* *:*:*/5
cron-on-error = continue
restart = false
EOF

slinitctl --system start "$SVC" >/dev/null 2>&1
wait_for_service "$SVC" "STARTED" 10 || true
assert_service_state "$SVC" "STARTED" "$SVC STARTED (cron-calendar accepted)"

# A 12-second window guarantees we cross at least 2 multiples of 5
# regardless of the second-of-minute we started in (worst case: started
# at sec=1 → hits at 5, 10 within 12s).
sleep 12

_TESTS_RUN=$((_TESTS_RUN + 1))
_n=$(wc -l < "$MARK" 2>/dev/null | tr -d ' ')
if [ -z "$_n" ]; then _n=0; fi
if [ "$_n" -ge 2 ]; then
    echo "OK: cron-calendar fired $_n times in 12s (expected >=2)"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: cron-calendar fired only $_n times in 12s (expected >=2)"
fi

# Static-parse coverage via slinit-check: probe the alias forms and a
# couple of invalid expressions. We deploy throwaway files in /tmp/
# (not /etc/slinit.d) so they don't mix with the live daemon.
_CHECKDIR="/tmp/acceptance-test-cron-cal-check"
mkdir -p "$_CHECKDIR"
trap '
    svc_remove "$SVC"
    rm -f "$MARK"
    rm -rf "$_CHECKDIR"
' EXIT INT TERM

# slinit-check takes service NAMES (not paths) and resolves them within
# -d directories — give it the basename and tell it where to look.
for _expr in "minutely" "hourly" "daily" "weekly" "Mon..Fri 09:00" "*-*-* 00:00:00"; do
    cat > "$_CHECKDIR/svc-good" <<EOF2
type = process
command = /bin/true
cron-command = /bin/true
cron-calendar = $_expr
EOF2
    _TESTS_RUN=$((_TESTS_RUN + 1))
    if slinit-check -d "$_CHECKDIR" svc-good >/dev/null 2>&1; then
        echo "OK: slinit-check accepts cron-calendar '$_expr'"
    else
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: slinit-check rejected valid cron-calendar '$_expr'"
    fi
done

# Empty value can't be tested via the file form (the parser would not
# accept "cron-calendar = " as a key=value line — it skips empty values),
# so the malformed set focuses on syntactically-present but invalid
# expressions.
for _expr in "Mon..Funday 09:00" "*-99-* 00:00:00" "99:00"; do
    cat > "$_CHECKDIR/svc-bad" <<EOF2
type = process
command = /bin/true
cron-command = /bin/true
cron-calendar = $_expr
EOF2
    _TESTS_RUN=$((_TESTS_RUN + 1))
    if slinit-check -d "$_CHECKDIR" svc-bad >/dev/null 2>&1; then
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: slinit-check accepted invalid cron-calendar '$_expr'"
    else
        echo "OK: slinit-check rejects invalid cron-calendar '$_expr'"
    fi
done

test_summary
