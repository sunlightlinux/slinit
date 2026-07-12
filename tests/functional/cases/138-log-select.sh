#!/bin/sh
# Test: log-select regex-chain filtering (s6-log analogue).
# `-* +alert +warn` drops everything by default but re-includes lines
# matching alert or warn. Chain semantics differ from log-include/
# log-exclude: last-matched verdict wins per line.

SVC="test-logselect"
LOG_DIR=/tmp/functional-logselect
LOG_FILE="$LOG_DIR/current"

mkdir -p "$LOG_DIR"

cat > "/etc/slinit.d/$SVC" <<EOF
type = process
command = /bin/sh -c 'printf "debug: chatty\\\\nalert: DISK FULL\\\\ninfo: fyi\\\\nwarn: low mem\\\\ntrace: verbose\\\\n"; sleep 30'
restart = false
logfile = $LOG_FILE
log-select = -* +alert +warn
EOF
slinitctl --system start "$SVC" >/dev/null 2>&1
wait_for_service "$SVC" "STARTED" 10 || true
assert_service_state "$SVC" "STARTED" "$SVC STARTED"

_e=0
while [ "$_e" -lt 5 ] && [ ! -s "$LOG_FILE" ]; do
    sleep 1; _e=$((_e + 1))
done
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ ! -s "$LOG_FILE" ]; then
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: log file empty at $LOG_FILE"
    test_summary
    return 1
fi

# Kept: alert + warn
assert_contains "$(cat "$LOG_FILE")" "DISK FULL" \
    "chain kept 'alert:' line"
assert_contains "$(cat "$LOG_FILE")" "low mem" \
    "chain kept 'warn:' line"

# Dropped: debug + info + trace (only matched `-*`, verdict = exclude)
assert_not_contains "$(cat "$LOG_FILE")" "chatty" \
    "chain dropped 'debug:' line"
assert_not_contains "$(cat "$LOG_FILE")" "fyi" \
    "chain dropped 'info:' line"
assert_not_contains "$(cat "$LOG_FILE")" "verbose" \
    "chain dropped 'trace:' line"

test_summary
