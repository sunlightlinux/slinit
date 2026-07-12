#!/bin/sh
# 149-sentinel-file-ipc — --sentinel-dir opts a nested slinit into
# runit-style sentinel-file shutdown IPC. Dropping an executable file
# named `reboot` under the watched dir fires the reboot handler,
# emits an audited log line, and unlinks the file. We watch for
# both the "sentinel: watching" wire-up line and the "sentinel:
# reboot requested" trigger line. The nested user daemon shuts
# itself down as a side-effect — which is the whole point.

NEST_ROOT=/tmp/acceptance-sentinel-$$
NEST_SOCK="$NEST_ROOT/sock"
NEST_SVCS="$NEST_ROOT/svcs"
NEST_LOG="$NEST_ROOT/log"
SENT_DIR="$NEST_ROOT/sentinel"
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

mkdir -p "$NEST_SVCS" "$SENT_DIR"
cat > "$NEST_SVCS/boot" <<'EOF'
type = internal
EOF

setsid /usr/bin/slinit --user \
    --socket-path "$NEST_SOCK" \
    --services-dir "$NEST_SVCS" \
    --log-file "$NEST_LOG" \
    --sentinel-dir "$SENT_DIR" \
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
    echo "FAIL: nested slinit did not start"
    test_summary
    return 1 2>/dev/null || exit 1
fi
echo "OK: nested slinit up"

# Watching-directory arm line.
_e=0
while [ "$_e" -lt 5 ] && ! grep -q 'sentinel: watching' "$NEST_LOG"; do
    sleep 1; _e=$((_e + 1))
done
assert_contains "$(cat "$NEST_LOG")" "sentinel: watching" \
    "sentinel watcher armed"

# Fire the reboot sentinel — plain touch first, then chmod +x, to
# reproduce runit's "flip the executable bit" convention. A plain
# touch alone should be silently ignored.
touch "$SENT_DIR/reboot"
sleep 1
assert_not_contains "$(cat "$NEST_LOG")" "sentinel: reboot requested" \
    "non-executable reboot file does NOT trigger"

chmod +x "$SENT_DIR/reboot"
# Give the inotify handler + audit log 3s to catch it.
_e=0
while [ "$_e" -lt 5 ] && \
      ! grep -q 'sentinel: reboot requested' "$NEST_LOG"; do
    sleep 1; _e=$((_e + 1))
done
assert_contains "$(cat "$NEST_LOG")" "sentinel: reboot requested" \
    "chmod +x reboot triggers the handler"

# Sentinel is unlinked after firing so it doesn't retrigger.
sleep 1
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ ! -e "$SENT_DIR/reboot" ]; then
    echo "OK: sentinel file unlinked after firing"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: sentinel file still present: $SENT_DIR/reboot"
fi

test_summary
