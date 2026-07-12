#!/bin/sh
# 148-log-forward-udp — log-forward-udp = host:port sends producer stdout
# lines to a UDP listener framed per RFC 3164 or RFC 5424. Start a netcat
# UDP listener on a scratch port, deploy a service whose stdout is
# forwarded, and grep the collected datagrams for the marker + framing.
# Requires `nc` (busybox variant on Void is fine).

if ! command -v nc >/dev/null 2>&1; then
    echo "SKIP: nc not available"
    test_summary
    return 0 2>/dev/null || exit 0
fi

SVC="acceptance-test-udplogfwd"
LOG_PORT=15140
LOG_OUT=/tmp/acceptance-udpfwd.$$.log
NC_PID=""
MARKER="UDPFWD_MARK_$$"

cleanup() {
    svc_remove "$SVC"
    if [ -n "$NC_PID" ] && kill -0 "$NC_PID" 2>/dev/null; then
        kill "$NC_PID" 2>/dev/null
    fi
    rm -f "$LOG_OUT"
}
trap cleanup EXIT INT TERM

# Persist the UDP receiver. `nc -lu -w0 -k` is BusyBox-safe (some
# variants don't support -k; we fall back to a one-shot receiver).
if nc -h 2>&1 | grep -q -- '-k'; then
    nc -u -l -p "$LOG_PORT" -k > "$LOG_OUT" 2>/dev/null &
    NC_PID=$!
else
    nc -u -l -p "$LOG_PORT" > "$LOG_OUT" 2>/dev/null &
    NC_PID=$!
fi
sleep 1
_TESTS_RUN=$((_TESTS_RUN + 1))
if ! kill -0 "$NC_PID" 2>/dev/null; then
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: UDP listener did not start on port $LOG_PORT"
    test_summary
    return 1 2>/dev/null || exit 1
fi
echo "OK: UDP listener up on port $LOG_PORT"

svc_deploy "$SVC" <<EOF
type = process
command = /bin/sh -c 'echo $MARKER; sleep 30'
restart = false
log-forward-udp = 127.0.0.1:$LOG_PORT
log-forward-format = rfc3164
log-forward-tag = acceptance-udpfwd
EOF
slinitctl --system start "$SVC" >/dev/null 2>&1
wait_for_service "$SVC" "STARTED" 10 || true
assert_service_state "$SVC" "STARTED" "$SVC STARTED"

# Give the forwarder up to 5s to drain the pipeline.
_e=0
while [ "$_e" -lt 5 ]; do
    if grep -q "$MARKER" "$LOG_OUT" 2>/dev/null; then
        break
    fi
    sleep 1; _e=$((_e + 1))
done

assert_contains "$(cat "$LOG_OUT" 2>/dev/null)" "$MARKER" \
    "UDP receiver caught the marker"
# RFC 3164 framing has "<PRI>MMM DD HH:MM:SS host tag: message"; the
# `<NUM>` prefix is the discriminator that proves the framing wrapper
# is active (raw stdout would not carry it).
assert_contains "$(cat "$LOG_OUT" 2>/dev/null)" "acceptance-udpfwd" \
    "framed message includes log-forward-tag"

test_summary
