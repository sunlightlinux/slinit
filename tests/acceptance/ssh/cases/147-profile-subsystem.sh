#!/bin/sh
# 147-profile-subsystem — activate-profile / active-profile / list-profiles
# on a nested slinit --user daemon. Two services carry disjoint `profile=`
# tags; both are explicitly LOADED (--user doesn't auto-load anything but
# the boot svc), then we drive the profile CLI subcommands and prove:
#   * list-profiles enumerates the loaded tags
#   * activate-profile validates against loaded services
#   * active-profile round-trips activated state
#   * '-' (or ""=deactivate) restores the empty filter
# runsvchdir replacement — commit e9e484e.

NEST_ROOT=/tmp/acceptance-profile-$$
NEST_SOCK="$NEST_ROOT/sock"
NEST_SVCS="$NEST_ROOT/svcs"
NEST_LOG="$NEST_ROOT/log"
NEST_PID=""

# Every slinitctl call is bounded so a stuck daemon can't wedge the
# suite — 5s is generous for a control-socket round-trip.
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
cat > "$NEST_SVCS/svc-desktop" <<'EOF'
type = internal
profile = desktop
EOF
cat > "$NEST_SVCS/svc-server" <<'EOF'
type = internal
profile = server
EOF

setsid /usr/bin/slinit --user \
    --socket-path "$NEST_SOCK" \
    --services-dir "$NEST_SVCS" \
    --log-file "$NEST_LOG" \
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

# Extra settle: some slinitctl commands race against DirLoader
# scanning the services-dir. 1s is enough for a fresh --user boot.
sleep 1

# Force-load the two profile-tagged services — --user auto-loads
# only the boot service, so without explicit start the profiles
# never enter the daemon's registry.
sl start svc-desktop >/dev/null 2>&1
sl start svc-server  >/dev/null 2>&1

# list-profiles must enumerate the two declared tags.
_profs=$(sl list-profiles 2>&1)
assert_contains "$_profs" "desktop" "list-profiles has 'desktop'"
assert_contains "$_profs" "server"  "list-profiles has 'server'"

# No profile active by default — active-profile prints empty.
_active=$(sl active-profile 2>&1)
_TESTS_RUN=$((_TESTS_RUN + 1))
case "$_active" in
    ""|"(no active profile)"*)
        echo "OK: active-profile empty by default (got '${_active}')" ;;
    *)
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: active-profile not empty by default: '$_active'" ;;
esac

# Activate 'desktop' — validates that at least one loaded service
# declares that profile.
_out=$(sl activate-profile desktop 2>&1)
assert_contains "$_out" "desktop" "activate-profile reports 'desktop'"

_active=$(sl active-profile 2>&1 | tr -d '\n\r ')
assert_eq "$_active" "desktop" "active-profile now returns 'desktop'"

# Deactivate (with '-') restores the empty filter.
sl activate-profile - >/dev/null 2>&1
_active=$(sl active-profile 2>&1)
_TESTS_RUN=$((_TESTS_RUN + 1))
case "$_active" in
    ""|"(no active profile)"*)
        echo "OK: active-profile empty after deactivate (got '${_active}')" ;;
    *)
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: active-profile not empty after deactivate: '$_active'" ;;
esac

# Typo-guard: activating an unknown profile must NAK, not silently
# stop every profile-tagged service.
_out=$(sl activate-profile nonexistent-typo 2>&1)
_rc=$?
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$_rc" -ne 0 ] || echo "$_out" | grep -qi 'no loaded service'; then
    echo "OK: unknown profile rejected: ${_out}"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: unknown profile unexpectedly accepted: rc=$_rc out=$_out"
fi

test_summary
