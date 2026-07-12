#!/bin/sh
# Test: protocol v7 CmdEnableServiceV7 returns the target's status in
# the same round-trip as the enable, and the reply carries dep_exists
# so `slinitctl enable` distinguishes "enabled" from "already enabled".
# Run against a nested slinit --user so we don't perturb PID 1.

NEST_ROOT=/tmp/functional-enablev7
NEST_SOCK="$NEST_ROOT/sock"
NEST_SVCS="$NEST_ROOT/svcs"

sl() {
    timeout 5 slinitctl --socket-path "$NEST_SOCK" "$@"
}

mkdir -p "$NEST_SVCS"
cat > "$NEST_SVCS/boot" <<'EOF'
type = internal
EOF
cat > "$NEST_SVCS/target" <<'EOF'
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

_out1=$(sl enable target 2>&1)
_rc1=$?
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$_rc1" -ne 0 ]; then
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: first enable rc=$_rc1: $_out1"
else
    echo "OK: first enable succeeded (out: $_out1)"
fi
assert_contains "$_out1" "enabled" "first enable reports 'enabled'"
_TESTS_RUN=$((_TESTS_RUN + 1))
case "$_out1" in
    *"already enabled"*)
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: first enable reported 'already enabled' (dep_exists byte wrong)" ;;
    *)
        echo "OK: first enable did not claim already-enabled" ;;
esac

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

kill "$NEST_PID" 2>/dev/null
test_summary
