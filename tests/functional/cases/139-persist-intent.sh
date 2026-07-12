#!/bin/sh
# Test: --persist-intent DIR persists pin transitions to disk.
# `stop --pin` writes DIR/<svc> with "pinned-stopped"; `unpin` removes
# the file. Full round-trip (survives daemon restart) can't be tested
# in a single VM boot, but the file-write side is observable and the
# code path is the same one the boot-time restore reads.

NEST_ROOT=/tmp/functional-persist
NEST_SOCK="$NEST_ROOT/sock"
NEST_SVCS="$NEST_ROOT/svcs"
INTENT_DIR="$NEST_ROOT/intent"

sl() {
    timeout 5 slinitctl --socket-path "$NEST_SOCK" "$@"
}

mkdir -p "$NEST_SVCS"
cat > "$NEST_SVCS/boot" <<'EOF'
type = internal
EOF
cat > "$NEST_SVCS/target" <<'EOF'
type = process
command = /bin/sh -c 'while :; do sleep 60; done'
restart = false
EOF

setsid /sbin/slinit --user \
    --socket-path "$NEST_SOCK" \
    --services-dir "$NEST_SVCS" \
    --persist-intent "$INTENT_DIR" \
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
echo "OK: nested slinit up with --persist-intent"

# Start the target, then stop --pin. The stop-pin path should persist
# the intent to disk under $INTENT_DIR/target.
sl start target >/dev/null 2>&1
sleep 1
sl stop --pin target >/dev/null 2>&1
sleep 1

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -f "$INTENT_DIR/target" ]; then
    echo "OK: intent file $INTENT_DIR/target created after stop --pin"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: intent file $INTENT_DIR/target missing after stop --pin"
    ls -la "$INTENT_DIR" 2>&1
fi

assert_contains "$(cat "$INTENT_DIR/target" 2>/dev/null)" "pinned-stopped" \
    "intent file records 'pinned-stopped'"

# Unpin should clear the intent — otherwise a subsequent boot would
# re-apply a pin the operator already removed.
sl unpin target >/dev/null 2>&1
sleep 1

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ ! -e "$INTENT_DIR/target" ]; then
    echo "OK: intent file removed after unpin"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: intent file lingers after unpin: $(cat "$INTENT_DIR/target")"
fi

# Start --pin should write the mirror intent.
sl start --pin target >/dev/null 2>&1
sleep 1
assert_contains "$(cat "$INTENT_DIR/target" 2>/dev/null)" "pinned-started" \
    "intent file records 'pinned-started' after start --pin"

kill "$NEST_PID" 2>/dev/null
test_summary
