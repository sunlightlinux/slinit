#!/bin/sh
# Test: --watch-services-dir arms an inotify rescan of the services-dir
# so new descriptions become loadable without an explicit reload-all.

NEST_ROOT=/tmp/functional-svcwatch
NEST_SOCK="$NEST_ROOT/sock"
NEST_SVCS="$NEST_ROOT/svcs"
NEST_LOG="$NEST_ROOT/log"

mkdir -p "$NEST_SVCS"
cat > "$NEST_SVCS/boot" <<'EOF'
type = internal
EOF

setsid /sbin/slinit --user \
    --socket-path "$NEST_SOCK" \
    --services-dir "$NEST_SVCS" \
    --log-file "$NEST_LOG" \
    --watch-services-dir \
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

_list=$(slinitctl --socket-path "$NEST_SOCK" list 2>&1)
assert_not_contains "$_list" "svcwatch-drop" \
    "list has no svcwatch-drop before it exists"

cat > "$NEST_SVCS/svcwatch-drop" <<'EOF'
type = internal
EOF
sleep 2

_out=$(slinitctl --socket-path "$NEST_SOCK" start svcwatch-drop 2>&1)
assert_not_contains "$_out" "not found" \
    "auto-detected service is startable after being dropped"

_list=$(slinitctl --socket-path "$NEST_SOCK" list 2>&1)
assert_contains "$_list" "svcwatch-drop" \
    "list now shows svcwatch-drop after inotify pickup"

kill "$NEST_PID" 2>/dev/null
test_summary
