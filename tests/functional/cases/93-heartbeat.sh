#!/bin/sh
# Test: --heartbeat-interval periodically emits a one-line health summary.
# Validates: heartbeat prefix + every documented counter reaches the log.
#
# Runs a nested `slinit --user` daemon so the PID-1 slinit stays
# undisturbed. Same self-contained pattern the acceptance version uses.

NEST_ROOT=/tmp/functional-heartbeat
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
    --heartbeat-interval 2s \
    </dev/null >/dev/null 2>&1 &
NEST_PID=$!

# Wait for the nested socket to appear (daemon fully up).
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

# One interval + margin for the first heartbeat.
sleep 6

_line=$(grep 'heartbeat:' "$NEST_LOG" | tail -1)
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -z "$_line" ]; then
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: no heartbeat: line in $NEST_LOG"
else
    echo "OK: heartbeat emitted: $_line"
fi

for _field in "active=" "failed=" "stopped=" "starting=" "stopping=" \
              "restarts(" "watchdog-misses=" "rss="; do
    assert_contains "$_line" "$_field" "heartbeat has $_field"
done

kill "$NEST_PID" 2>/dev/null
test_summary
