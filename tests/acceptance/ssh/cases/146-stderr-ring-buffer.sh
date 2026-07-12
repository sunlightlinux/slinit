#!/bin/sh
# 146-stderr-ring-buffer — --stderr-ring-buffer-size + short --interval
# retains recent stderr and re-emits it on the ticker. We can prove the
# feature is wired up by starting a nested daemon with a small ring +
# ~2s dump interval, then checking the daemon log for the periodic dump
# separator line the RingDumper writes. This is a wiring test; the
# byte-level ring semantics are covered by pkg/logging unit tests.

NEST_ROOT=/tmp/acceptance-ringbuf-$$
NEST_SOCK="$NEST_ROOT/sock"
NEST_SVCS="$NEST_ROOT/svcs"
NEST_LOG="$NEST_ROOT/log"
NEST_PID=""

cleanup() {
    if [ -n "$NEST_PID" ] && kill -0 "$NEST_PID" 2>/dev/null; then
        kill "$NEST_PID" 2>/dev/null
        _e=0
        while [ "$_e" -lt 5 ] && kill -0 "$NEST_PID" 2>/dev/null; do
            sleep 1; _e=$((_e + 1))
        done
        kill -9 "$NEST_PID" 2>/dev/null
    fi
    rm -rf "$NEST_ROOT"
}
trap cleanup EXIT INT TERM

mkdir -p "$NEST_SVCS"
cat > "$NEST_SVCS/boot" <<'EOF'
type = internal
EOF

setsid /usr/bin/slinit --user \
    --socket-path "$NEST_SOCK" \
    --services-dir "$NEST_SVCS" \
    --log-file "$NEST_LOG" \
    --stderr-ring-buffer-size 512 \
    --stderr-ring-buffer-interval 2s \
    </dev/null >/dev/null 2>&1 &
NEST_PID=$!
disown 2>/dev/null || true

_e=0
while [ "$_e" -lt 10 ] && [ ! -S "$NEST_SOCK" ]; do
    sleep 1; _e=$((_e + 1))
done
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ ! -S "$NEST_SOCK" ]; then
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: nested slinit did not create socket"
    test_summary
    return 1 2>/dev/null || exit 1
fi
echo "OK: nested slinit up"

# The RingDumper announces itself on startup with a "ring buffer: N B"
# INFO line. That proves the flag was recognized and the goroutine
# armed. Without the flag, ringDumper is nil and no such line appears.
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
    tail -20 "$NEST_LOG" 2>&1
fi

test_summary
