#!/bin/sh
# Test: svlogd -l analogue. Lines longer than log-max-line-length are
# truncated with a '+' overflow marker (32 content bytes + '+' + '\n').

SVC="test-logmaxline"
LOG_DIR=/tmp/functional-logmaxline
LOG_FILE="$LOG_DIR/current"

mkdir -p "$LOG_DIR"

_bigx=$(head -c 92 /dev/zero | tr "\0" "x")
cat > "/etc/slinit.d/$SVC" <<EOF
type = process
command = /bin/sh -c 'printf "MARKPFX %s\\\\n" "$_bigx"; sleep 30'
restart = false
logfile = $LOG_FILE
log-max-line-length = 32
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
    echo "FAIL: log file empty"
    test_summary
    return 1
fi

_maxlen=$(awk '{ if (length > m) m = length } END { print m+0 }' "$LOG_FILE")
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$_maxlen" -le 33 ]; then
    echo "OK: longest line $_maxlen ≤ cap+overflow-marker (33)"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: longest line $_maxlen > cap 33"
fi
assert_contains "$(cat "$LOG_FILE")" "MARKPFX" \
    "truncated line still carries the head marker"

test_summary
