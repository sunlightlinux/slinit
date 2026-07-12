#!/bin/sh
# Test: log-forward-udp = host:port sends producer stdout to a UDP
# listener framed per RFC 3164 / 5424. Prove the wire path is
# active by grepping datagrams collected by a scratch `nc` listener.
# Requires nc (busybox on Alpine is fine).

if ! command -v nc >/dev/null 2>&1; then
    echo "SKIP: nc not available"
    test_summary
    return 0
fi

SVC="test-udplogfwd"
LOG_PORT=15140
LOG_OUT=/tmp/functional-udpfwd.log
MARKER="UDPFWD_MARK"

if nc -h 2>&1 | grep -q -- '-k'; then
    nc -u -l -p "$LOG_PORT" -k > "$LOG_OUT" 2>/dev/null &
else
    nc -u -l -p "$LOG_PORT" > "$LOG_OUT" 2>/dev/null &
fi
NC_PID=$!
sleep 1
_TESTS_RUN=$((_TESTS_RUN + 1))
if ! kill -0 "$NC_PID" 2>/dev/null; then
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: UDP listener did not start on port $LOG_PORT"
    test_summary
    return 1
fi
echo "OK: UDP listener up on port $LOG_PORT"

cat > "/etc/slinit.d/$SVC" <<EOF
type = process
command = /bin/sh -c 'echo $MARKER; sleep 30'
restart = false
log-forward-udp = 127.0.0.1:$LOG_PORT
log-forward-format = rfc3164
log-forward-tag = functional-udpfwd
EOF
slinitctl --system start "$SVC" >/dev/null 2>&1
wait_for_service "$SVC" "STARTED" 10 || true
assert_service_state "$SVC" "STARTED" "$SVC STARTED"

_e=0
while [ "$_e" -lt 5 ]; do
    grep -q "$MARKER" "$LOG_OUT" 2>/dev/null && break
    sleep 1; _e=$((_e + 1))
done

assert_contains "$(cat "$LOG_OUT" 2>/dev/null)" "$MARKER" \
    "UDP receiver caught the marker"
assert_contains "$(cat "$LOG_OUT" 2>/dev/null)" "functional-udpfwd" \
    "framed message includes log-forward-tag"

kill "$NC_PID" 2>/dev/null
test_summary
