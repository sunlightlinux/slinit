#!/bin/sh
# Test: --stderr-ring-buffer-size + --stderr-ring-buffer-interval
# arms the daemon's own recent-log-line ring. We prove wiring by
# looking for the "ring buffer: N B" arm-line the RingDumper emits
# on startup; byte-level ring semantics have pkg/logging coverage.

NEST_ROOT=/tmp/functional-ringbuf
NEST_SOCK="$NEST_ROOT/sock"
NEST_SVCS="$NEST_ROOT/svcs"
NEST_LOG="$NEST_ROOT/log"

mkdir -p "$NEST_SVCS"
cat > "$NEST_SVCS/boot" <<'EOF'
type = internal
EOF

setsid /sbin/slinit --user \
    --socket-path "$NEST_SOCK" \
    --services-dir "$NEST_SVCS" \
    --log-file "$NEST_LOG" \
    --stderr-ring-buffer-size 512 \
    --stderr-ring-buffer-interval 2s \
    </dev/null >/dev/null 2>&1 &
NEST_PID=$!

_e=0
while [ "$_e" -lt 10 ] && [ ! -S "$NEST_SOCK" ]; do
    sleep 1; _e=$((_e + 1))
done
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ ! -S "$NEST_SOCK" ]; then
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: nested slinit did not create socket"
    kill -9 "$NEST_PID" 2>/dev/null
    test_summary
    return 1
fi
echo "OK: nested slinit up"

sleep 3
_e=0
while [ "$_e" -lt 10 ]; do
    if grep -q 'ring buffer' "$NEST_LOG" 2>/dev/null; then
        break
    fi
    sleep 1; _e=$((_e + 1))
done

_TESTS_RUN=$((_TESTS_RUN + 1))
if grep -q 'ring buffer' "$NEST_LOG"; then
    echo "OK: ring buffer arm line found in log"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: no 'ring buffer' arm line in $NEST_LOG"
fi

kill "$NEST_PID" 2>/dev/null
test_summary
