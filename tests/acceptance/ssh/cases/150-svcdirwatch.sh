#!/bin/sh
# 150-svcdirwatch — --watch-services-dir arms an inotify-based rescan
# of the services-dir so new descriptions become loadable without
# an explicit reload. Verified against a nested slinit --user
# instance: drop a new svc conf mid-run, then `list` picks it up
# without invoking `reload-all` first.

NEST_ROOT=/tmp/acceptance-svcwatch-$$
NEST_SOCK="$NEST_ROOT/sock"
NEST_SVCS="$NEST_ROOT/svcs"
NEST_LOG="$NEST_ROOT/log"
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
    --log-file "$NEST_LOG" \
    --watch-services-dir \
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

# Baseline: only boot is present.
_list=$(slinitctl --socket-path "$NEST_SOCK" list 2>&1)
assert_not_contains "$_list" "svcwatch-drop" \
    "list has no svcwatch-drop before it exists"

# Drop a new conf into the watched dir. svcdirwatch debounces
# inotify events over ~200ms, so 1s of settling time is plenty.
cat > "$NEST_SVCS/svcwatch-drop" <<'EOF'
type = internal
EOF
sleep 2

# Even without a manual reload, `slinitctl start` should now be
# able to resolve the new name. The DirLoader is the interface
# used, and svcdirwatch invalidates its cache on file-appear so
# the next lookup re-reads.
_out=$(slinitctl --socket-path "$NEST_SOCK" start svcwatch-drop 2>&1)
assert_not_contains "$_out" "not found" \
    "auto-detected service is startable after being dropped"

# And it now shows in `list`.
_list=$(slinitctl --socket-path "$NEST_SOCK" list 2>&1)
assert_contains "$_list" "svcwatch-drop" \
    "list now shows svcwatch-drop after inotify pickup"

test_summary
