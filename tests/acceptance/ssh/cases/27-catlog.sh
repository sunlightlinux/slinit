#!/bin/sh
# 27-catlog — `log-type = buffer` captures the service's stdout/stderr into
# an in-memory ring buffer; `slinitctl catlog SVC` dumps it. Probe: emit a
# known marker, then read it back.

SVC="acceptance-test-catlog"
MARKER="HELLO_FROM_$$_$(date +%s)"

trap 'svc_remove "$SVC"' EXIT INT TERM

svc_deploy "$SVC" <<EOF
type = process
log-type = buffer
command = /bin/sh -c 'echo $MARKER; while :; do sleep 60; done'
restart = false
EOF

slinitctl --system start "$SVC" >/dev/null 2>&1
wait_for_service "$SVC" "STARTED" 10 || true
assert_service_state "$SVC" "STARTED" "$SVC STARTED"

# Give the echo a tick to reach the buffer.
sleep 1

_log=$(slinitctl --system catlog "$SVC" 2>/dev/null)
assert_contains "$_log" "$MARKER" "catlog captured the marker"

# --clear flushes the buffer; a re-read must NOT contain the marker again.
slinitctl --system catlog --clear "$SVC" >/dev/null 2>&1
_log2=$(slinitctl --system catlog "$SVC" 2>/dev/null)
assert_not_contains "$_log2" "$MARKER" "catlog --clear flushed the buffer"

test_summary
