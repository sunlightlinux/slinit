#!/bin/sh
# Test: client-side preflight in `slinitctl shutdown kexec` warns when
# /sys/kernel/kexec_loaded=0. Route through a nested slinit --user so
# the daemon-side shutdown doesn't affect the QEMU host PID 1.

if [ -r /sys/kernel/kexec_loaded ] && \
   [ "$(cat /sys/kernel/kexec_loaded)" = "1" ]; then
    echo "SKIP: a kexec kernel is already loaded — preflight is silent by design"
    test_summary
    return 0
fi

NEST_ROOT=/tmp/functional-kexec
NEST_SOCK="$NEST_ROOT/sock"
NEST_SVCS="$NEST_ROOT/svcs"

mkdir -p "$NEST_SVCS"
cat > "$NEST_SVCS/boot" <<'EOF'
type = internal
EOF

setsid /sbin/slinit --user \
    --socket-path "$NEST_SOCK" \
    --services-dir "$NEST_SVCS" \
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

_err=$(timeout 5 slinitctl --socket-path "$NEST_SOCK" shutdown kexec now 2>&1 >/dev/null)

assert_contains "$_err" "no kexec kernel loaded" \
    "preflight warning fires when kexec_loaded=0"
assert_contains "$_err" "fall back" \
    "warning explains the fallback-to-reboot behavior"
assert_contains "$_err" "kexec -l" \
    "warning tells operator how to actually pre-load a kernel"

_e=0
while [ "$_e" -lt 5 ] && kill -0 "$NEST_PID" 2>/dev/null; do
    sleep 1; _e=$((_e + 1))
done
_TESTS_RUN=$((_TESTS_RUN + 1))
if ! kill -0 "$NEST_PID" 2>/dev/null; then
    echo "OK: nested daemon exited after 'shutdown kexec now' (no kernel effect)"
else
    echo "INFO: nested daemon still up"
    kill "$NEST_PID" 2>/dev/null
fi

test_summary
