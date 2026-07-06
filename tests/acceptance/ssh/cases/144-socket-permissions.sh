#!/bin/sh
# 144-socket-permissions — socket-activation listener socket gets the
# requested mode.

SVC="${ACCEPTANCE_NS_PREFIX}sockp"
SOCK="/tmp/acceptance-sockp.sock"

cleanup() {
    svc_remove "$SVC"
    rm -f "$SOCK"
}
trap cleanup EXIT INT TERM
cleanup

svc_deploy "$SVC" <<EOF
type = process
socket-listen = $SOCK
socket-permissions = 0640
socket-activation = on-demand
command = /bin/sh -c 'while :; do sleep 60; done'
restart = no
EOF

slinitctl --system start "$SVC" 2>/dev/null
# Path-activated sockets bind before the service starts. Give slinit
# ~2s to bind and then check mode.
sleep 2

_mode=$(stat -c '%a' "$SOCK" 2>/dev/null)
_TESTS_RUN=$((_TESTS_RUN + 1))
case "$_mode" in
    640)
        echo "OK: $SOCK mode=$_mode"
        ;;
    "")
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: $SOCK not created"
        ;;
    *)
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: mode=$_mode (expected 640)"
        ;;
esac

test_summary
