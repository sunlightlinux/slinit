#!/bin/sh
# Test: socket-permissions = 0640 sets the listener socket mode.

SVC="test-sockp"
SOCK="/tmp/functional-sockp.sock"

cat > "/etc/slinit.d/$SVC" <<EOF
type = process
socket-listen = $SOCK
socket-permissions = 0640
socket-activation = on-demand
command = /bin/sh -c 'while :; do sleep 60; done'
restart = no
EOF

slinitctl --system start "$SVC" 2>/dev/null
sleep 2

_mode=$(stat -c '%a' "$SOCK" 2>/dev/null)
_TESTS_RUN=$((_TESTS_RUN + 1))
case "$_mode" in
    640) echo "OK: $SOCK mode=$_mode" ;;
    "") _TESTS_FAILED=$((_TESTS_FAILED + 1)); echo "FAIL: $SOCK not created" ;;
    *)  _TESTS_FAILED=$((_TESTS_FAILED + 1)); echo "FAIL: mode=$_mode, expected 640" ;;
esac

test_summary
