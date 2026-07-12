#!/bin/sh
# Test: --sentinel-dir opts a nested slinit into runit-style
# sentinel-file shutdown IPC. Chmod +x on a `reboot` file under the
# watched dir must fire the reboot handler, emit an audit line, and
# unlink the file. A plain (non-executable) touch must not trigger.

NEST_ROOT=/tmp/functional-sentinel
NEST_SOCK="$NEST_ROOT/sock"
NEST_SVCS="$NEST_ROOT/svcs"
NEST_LOG="$NEST_ROOT/log"
SENT_DIR="$NEST_ROOT/sentinel"

mkdir -p "$NEST_SVCS" "$SENT_DIR"
cat > "$NEST_SVCS/boot" <<'EOF'
type = internal
EOF

setsid /sbin/slinit --user \
    --socket-path "$NEST_SOCK" \
    --services-dir "$NEST_SVCS" \
    --log-file "$NEST_LOG" \
    --sentinel-dir "$SENT_DIR" \
    </dev/null >/dev/null 2>&1 &
NEST_PID=$!

_e=0
while [ "$_e" -lt 10 ] && [ ! -S "$NEST_SOCK" ]; do
    sleep 1; _e=$((_e + 1))
done
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ ! -S "$NEST_SOCK" ]; then
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: nested slinit did not start"
    kill -9 "$NEST_PID" 2>/dev/null
    test_summary
    return 1
fi
echo "OK: nested slinit up"

_e=0
while [ "$_e" -lt 5 ] && ! grep -q 'sentinel: watching' "$NEST_LOG"; do
    sleep 1; _e=$((_e + 1))
done
assert_contains "$(cat "$NEST_LOG")" "sentinel: watching" \
    "sentinel watcher armed"

touch "$SENT_DIR/reboot"
sleep 1
assert_not_contains "$(cat "$NEST_LOG")" "sentinel: reboot requested" \
    "non-executable reboot file does NOT trigger"

chmod +x "$SENT_DIR/reboot"
_e=0
while [ "$_e" -lt 5 ] && \
      ! grep -q 'sentinel: reboot requested' "$NEST_LOG"; do
    sleep 1; _e=$((_e + 1))
done
assert_contains "$(cat "$NEST_LOG")" "sentinel: reboot requested" \
    "chmod +x reboot triggers the handler"

sleep 1
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ ! -e "$SENT_DIR/reboot" ]; then
    echo "OK: sentinel file unlinked after firing"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: sentinel file still present"
fi

kill "$NEST_PID" 2>/dev/null
test_summary
