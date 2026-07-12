#!/bin/sh
# 161-kexec-preflight — commit 3857180 (v1.10.37). Client-side
# preflight in `slinitctl shutdown kexec` warns the operator when
# /sys/kernel/kexec_loaded == 0 so they know the daemon will fall
# back to a normal reboot instead of silently wedging.
#
# Testing this on a live PID-1 slinit is unsafe: even a scheduled
# `+2h` kexec was observed to correlate with mid-suite reboots on
# ceres. Route the check through a NESTED slinit --user daemon
# instead — sending `shutdown kexec now` there stops the nested
# manager (no kernel-level effect) so the host stays up regardless
# of what the daemon does with the request.
#
# What we're actually asserting: the CLIENT-side preflight fires
# BEFORE the daemon handles the request. The nested daemon is only
# a way to satisfy the client's connect + handshake so
# cmdShutdownDispatch actually runs.

# Guardrail: if a kexec kernel IS loaded, the preflight is silent
# on purpose — nothing to test.
if [ -r /sys/kernel/kexec_loaded ] && \
   [ "$(cat /sys/kernel/kexec_loaded)" = "1" ]; then
    echo "SKIP: a kexec kernel is already loaded — preflight is silent by design"
    test_summary
    return 0 2>/dev/null || exit 0
fi

NEST_ROOT=/tmp/acceptance-kexec-$$
NEST_SOCK="$NEST_ROOT/sock"
NEST_SVCS="$NEST_ROOT/svcs"
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

# Preflight fires INSIDE slinitctl before any packet is sent. The
# nested daemon will subsequently receive CmdShutdown (immediate);
# because it is not PID 1, it just stops its own services and exits.
_err=$(timeout 5 slinitctl --socket-path "$NEST_SOCK" shutdown kexec now 2>&1 >/dev/null)

assert_contains "$_err" "no kexec kernel loaded" \
    "preflight warning fires when kexec_loaded=0"
assert_contains "$_err" "fall back" \
    "warning explains the fallback-to-reboot behavior"
assert_contains "$_err" "kexec -l" \
    "warning tells operator how to actually pre-load a kernel"

# Give the nested daemon a moment to exit; if it doesn't, cleanup
# will SIGTERM it anyway.
_e=0
while [ "$_e" -lt 5 ] && kill -0 "$NEST_PID" 2>/dev/null; do
    sleep 1; _e=$((_e + 1))
done
_TESTS_RUN=$((_TESTS_RUN + 1))
if ! kill -0 "$NEST_PID" 2>/dev/null; then
    echo "OK: nested daemon exited after 'shutdown kexec now' (no kernel effect)"
else
    # Not fatal — the client-side warning is what we're really
    # testing; the nested daemon's exit path is a bonus proof.
    echo "INFO: nested daemon still up (cleanup will terminate it)"
fi

test_summary
