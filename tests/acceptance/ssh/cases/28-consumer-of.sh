#!/bin/sh
# 28-consumer-of — `consumer-of: PRODUCER` makes slinit pipe the producer
# service's stdout (it needs `log-type = pipe`) into the consumer's stdin.
# Probe: PRODUCER emits a known token; CONSUMER is `cat` with
# `log-type = buffer`, so anything it reads on stdin lands in its log
# buffer; we read the buffer back via `slinitctl catlog`.
#
# Notes:
#  - consumer-of is wiring only (pkg/config/loader.go setupConsumerOf does
#    SetLogConsumer + SetConsumerFor with no implicit start), so both ends
#    must be requested explicitly.
#  - Reading lines through a shell `while read` loop loses content in this
#    setup — empty lines came through in early tries while a bare `cat`
#    pass-through carried the bytes verbatim. The pure-cat probe is the
#    one we keep.

PRODUCER="acceptance-test-producer"
CONSUMER="acceptance-test-consumer"
TOKEN="TOKEN-$(date +%s)-$$"

cleanup() {
    svc_remove "$CONSUMER" "$PRODUCER"
}
trap cleanup EXIT INT TERM

svc_deploy "$PRODUCER" <<EOF
type = process
log-type = pipe
command = /bin/sh -c 'echo $TOKEN; sleep 60'
restart = false
EOF

svc_deploy "$CONSUMER" <<EOF
type = process
consumer-of: $PRODUCER
log-type = buffer
command = /bin/cat
restart = false
EOF

slinitctl --system start "$CONSUMER" >/dev/null 2>&1
wait_for_service "$CONSUMER" "STARTED" 10 || true
assert_service_state "$CONSUMER" "STARTED" "$CONSUMER STARTED"

slinitctl --system start "$PRODUCER" >/dev/null 2>&1
wait_for_service "$PRODUCER" "STARTED" 10 || true
assert_service_state "$PRODUCER" "STARTED" "$PRODUCER STARTED"

# Give the producer a moment to emit and the buffer a tick to flush.
sleep 2

_log=$(slinitctl --system catlog "$CONSUMER" 2>/dev/null)
assert_contains "$_log" "$TOKEN" "consumer received producer's stdout"

# Sanity: producer and consumer share a pipe inode (proves the wiring).
_p_pid=$(slinitctl --system status "$PRODUCER" 2>/dev/null | awk '/PID:/ {print $2}')
_c_pid=$(slinitctl --system status "$CONSUMER" 2>/dev/null | awk '/PID:/ {print $2}')
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -n "$_p_pid" ] && [ -n "$_c_pid" ]; then
    _p_stdout=$(readlink "/proc/$_p_pid/fd/1" 2>/dev/null)
    _c_stdin=$(readlink "/proc/$_c_pid/fd/0" 2>/dev/null)
    if [ -n "$_p_stdout" ] && [ "$_p_stdout" = "$_c_stdin" ]; then
        echo "OK: producer fd1 and consumer fd0 share '$_p_stdout'"
    else
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: pipe mismatch: producer.fd1=$_p_stdout consumer.fd0=$_c_stdin"
    fi
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: missing pid: producer=$_p_pid consumer=$_c_pid"
fi

test_summary
