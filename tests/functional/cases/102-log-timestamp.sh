#!/bin/sh
# Test: svlogd -tt analogue. log-timestamp=human prepends
# YYYY-MM-DD_HH:MM:SS.µs to every logged line.

SVC="test-logts"
LOG_DIR=/tmp/functional-logts
LOG_FILE="$LOG_DIR/current"

mkdir -p "$LOG_DIR"

cat > "/etc/slinit.d/$SVC" <<EOF
type = process
command = /bin/sh -c 'echo TSMARK; sleep 30'
restart = false
logfile = $LOG_FILE
log-timestamp = human
EOF
slinitctl --system start "$SVC" >/dev/null 2>&1
wait_for_service "$SVC" "STARTED" 10 || true
assert_service_state "$SVC" "STARTED" "$SVC STARTED"

_e=0
while [ "$_e" -lt 5 ] && ! grep -q TSMARK "$LOG_FILE" 2>/dev/null; do
    sleep 1; _e=$((_e + 1))
done
_TESTS_RUN=$((_TESTS_RUN + 1))
if ! grep -q TSMARK "$LOG_FILE" 2>/dev/null; then
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: marker line never landed"
    test_summary
    return 1
fi

_line=$(grep TSMARK "$LOG_FILE" | head -1)
_TESTS_RUN=$((_TESTS_RUN + 1))
if echo "$_line" | grep -qE '^[0-9]{4}-[0-9]{2}-[0-9]{2}[T _][0-9]{2}:[0-9]{2}:[0-9]{2}'; then
    echo "OK: line carries human timestamp prefix: $_line"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: line lacks human timestamp: $_line"
fi

test_summary
