#!/bin/sh
# Test: activate-profile / active-profile / list-profiles on a nested
# slinit --user daemon. Runsvchdir-style dispatch. Guards against the
# reply-code collision that shipped and hung the whole subsystem —
# without a functional watch here that regression could ship silently.

NEST_ROOT=/tmp/functional-profile
NEST_SOCK="$NEST_ROOT/sock"
NEST_SVCS="$NEST_ROOT/svcs"
NEST_LOG="$NEST_ROOT/log"

sl() {
    timeout 5 slinitctl --socket-path "$NEST_SOCK" "$@"
}

mkdir -p "$NEST_SVCS"
cat > "$NEST_SVCS/boot" <<'EOF'
type = internal
EOF
cat > "$NEST_SVCS/svc-desktop" <<'EOF'
type = internal
profile = desktop
EOF
cat > "$NEST_SVCS/svc-server" <<'EOF'
type = internal
profile = server
EOF

setsid /sbin/slinit --user \
    --socket-path "$NEST_SOCK" \
    --services-dir "$NEST_SVCS" \
    --log-file "$NEST_LOG" \
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
sleep 1

# --user auto-loads only the boot svc — force-load the profile-tagged pair.
sl start svc-desktop >/dev/null 2>&1
sl start svc-server  >/dev/null 2>&1

_profs=$(sl list-profiles 2>&1)
assert_contains "$_profs" "desktop" "list-profiles has 'desktop'"
assert_contains "$_profs" "server"  "list-profiles has 'server'"

_active=$(sl active-profile 2>&1)
_TESTS_RUN=$((_TESTS_RUN + 1))
case "$_active" in
    ""|"(no active profile)"*)
        echo "OK: active-profile empty by default" ;;
    *)
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: active-profile not empty by default: '$_active'" ;;
esac

_out=$(sl activate-profile desktop 2>&1)
assert_contains "$_out" "desktop" "activate-profile reports 'desktop'"

_active=$(sl active-profile 2>&1 | tr -d '\n\r ')
assert_eq "$_active" "desktop" "active-profile now returns 'desktop'"

sl activate-profile - >/dev/null 2>&1
_active=$(sl active-profile 2>&1)
_TESTS_RUN=$((_TESTS_RUN + 1))
case "$_active" in
    ""|"(no active profile)"*)
        echo "OK: active-profile empty after deactivate" ;;
    *)
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: active-profile not empty after deactivate: '$_active'" ;;
esac

_out=$(sl activate-profile nonexistent-typo 2>&1)
_rc=$?
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$_rc" -ne 0 ] || echo "$_out" | grep -qi 'no loaded service'; then
    echo "OK: unknown profile rejected"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: unknown profile unexpectedly accepted: rc=$_rc out=$_out"
fi

kill "$NEST_PID" 2>/dev/null
test_summary
