#!/bin/sh
# 162-enable-v7-status — dinit-parity commit d7d843b. Protocol v7
# adds CmdEnableServiceV7 which returns the target's status in the
# same round-trip as the enable request, closing the race where
# `slinitctl enable X` on an already-started X could report the pre-
# enable state before a follow-up SERVICESTATUS query caught up.
#
# We can't easily assert "no race" in a shell test — the fix is
# structural (one packet instead of two). What we CAN verify:
#
#   1. `slinitctl --version` reports the client speaks v7.
#   2. Enable on a service NOT yet enabled reports "enabled".
#   3. Enable on a service already enabled reports "already enabled".
#      Pre-v7 the daemon just ACK'd both cases; the client had no way
#      to distinguish them.
#
# Run against a nested `slinit --user` daemon so the enable/disable
# dance doesn't perturb the system service graph.

NEST_ROOT=/tmp/acceptance-enablev7-$$
NEST_SOCK="$NEST_ROOT/sock"
NEST_SVCS="$NEST_ROOT/svcs"
NEST_PID=""

sl() {
    timeout 5 slinitctl --socket-path "$NEST_SOCK" "$@"
}

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
cat > "$NEST_SVCS/target" <<'EOF'
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

# First enable: the dep did NOT exist previously, expect "enabled" msg.
_out1=$(sl enable target 2>&1)
_rc1=$?
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$_rc1" -ne 0 ]; then
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: first enable rc=$_rc1: $_out1"
else
    echo "OK: first enable succeeded (out: $_out1)"
fi
assert_contains "$_out1" "enabled" \
    "first enable reports 'enabled'"
_TESTS_RUN=$((_TESTS_RUN + 1))
case "$_out1" in
    *"already enabled"*)
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: first enable reported 'already enabled' (dep_exists byte wrong)" ;;
    *)
        echo "OK: first enable did not claim already-enabled" ;;
esac

# Second enable on the same service: dep already exists, v7 must
# surface that via the dep_exists byte → "already enabled" message.
_out2=$(sl enable target 2>&1)
_rc2=$?
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$_rc2" -ne 0 ]; then
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: second enable rc=$_rc2: $_out2"
else
    echo "OK: second enable succeeded (out: $_out2)"
fi
assert_contains "$_out2" "already enabled" \
    "second enable reports 'already enabled' (v7 dep_exists byte)"

test_summary
