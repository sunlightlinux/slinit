#!/bin/sh
# 145-heartbeat — --heartbeat-interval periodically emits a one-line
# health summary. Spawn a nested slinit --user daemon with a short
# interval + log-file, wait for at least one heartbeat, and pattern-
# match the counters. Uses a scratch socket + services-dir so the
# PID-1 daemon is not disturbed.

NEST_ROOT=/tmp/acceptance-heartbeat-$$
NEST_SOCK="$NEST_ROOT/sock"
NEST_SVCS="$NEST_ROOT/svcs"
NEST_LOG="$NEST_ROOT/log"
NEST_PID=""

cleanup() {
    if [ -n "$NEST_PID" ] && kill -0 "$NEST_PID" 2>/dev/null; then
        kill "$NEST_PID" 2>/dev/null
        # Give the daemon a beat to unlink its socket cleanly.
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
# Minimal boot service so --user startup succeeds.
cat > "$NEST_SVCS/boot" <<'EOF'
type = internal
EOF

# 2s interval so a 6s wait guarantees ≥2 emissions even under load.
setsid /usr/bin/slinit --user \
    --socket-path "$NEST_SOCK" \
    --services-dir "$NEST_SVCS" \
    --log-file "$NEST_LOG" \
    --heartbeat-interval 2s \
    </dev/null >/dev/null 2>&1 &
NEST_PID=$!
disown 2>/dev/null || true

# Wait for the socket to appear (daemon fully up).
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

# One interval + margin for the first heartbeat.
sleep 6

_line=$(grep 'heartbeat:' "$NEST_LOG" | tail -1)
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -z "$_line" ]; then
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: no heartbeat: line in $NEST_LOG"
    tail -20 "$NEST_LOG" 2>&1
else
    echo "OK: heartbeat emitted: $_line"
fi

# Verify all documented counters are present.
for _field in "active=" "failed=" "stopped=" "starting=" "stopping=" \
              "restarts(" "watchdog-misses=" "rss="; do
    assert_contains "$_line" "$_field" "heartbeat has $_field"
done

test_summary
