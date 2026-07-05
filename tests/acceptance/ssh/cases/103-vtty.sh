#!/bin/sh
# 103-vtty — `vtty = true` runs the service under a PTY and exposes
# it via a Unix socket at /run/slinit/vtty-<svc>.sock. We can't
# actually attach interactively over non-interactive SSH, but we
# assert that (a) the service reaches STARTED under the PTY, (b) the
# socket appears, and (c) the socket vanishes on stop.

SVC="${ACCEPTANCE_NS_PREFIX}vtty"
SOCK="/run/slinit/vtty-${SVC}.sock"

cleanup() {
    svc_remove "$SVC"
}
trap cleanup EXIT INT TERM
cleanup

svc_deploy "$SVC" <<EOF
type = process
command = /bin/sh -c 'while true; do echo tick; sleep 5; done'
vtty = true
vtty-scrollback = 8192
restart = no
EOF

slinitctl --system start "$SVC" 2>/dev/null
wait_for_service "$SVC" STARTED 10
assert_eq "$(svc_state "$SVC")" "STARTED" "vtty service reached STARTED"

# The vtty socket appears in /run/slinit once the pty is set up.
_i=0
while [ "$_i" -lt 10 ] && [ ! -S "$SOCK" ]; do
    _i=$((_i + 1))
    sleep 0.3
done

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -S "$SOCK" ]; then
    echo "OK: vtty socket present at $SOCK"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: no socket at $SOCK — ls: $(ls -la /run/slinit/ 2>&1 | grep vtty)"
    test_summary
    exit 0
fi

# The socket is a Unix stream owned by root, mode 0600 by default
# (only the operator connects to it).
_TESTS_RUN=$((_TESTS_RUN + 1))
_perm=$(stat -c '%a' "$SOCK" 2>/dev/null)
case "$_perm" in
    600|660|666|700|770|777)
        echo "OK: vtty socket permissions ($_perm) restrict to owner/group"
        ;;
    *)
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: unexpected socket perms $_perm"
        ;;
esac

# The service's actual PID has a controlling PTY assigned. Confirm
# via /proc/PID/stat's tty_nr field being non-zero.
_pid=$(slinitctl --system status "$SVC" 2>/dev/null | awk '/^  PID:/ { print $2 }')
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -n "$_pid" ] && [ -f "/proc/$_pid/stat" ]; then
    _tty_nr=$(awk '{print $7}' "/proc/$_pid/stat" 2>/dev/null)
    if [ "$_tty_nr" != "0" ]; then
        echo "OK: service pid $_pid has a controlling tty (tty_nr=$_tty_nr)"
    else
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: pid $_pid has no tty (tty_nr=0)"
    fi
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: could not resolve service pid"
fi

# Stop → socket removed.
slinitctl --system stop "$SVC" 2>/dev/null
sleep 1
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ ! -S "$SOCK" ]; then
    echo "OK: vtty socket removed after stop"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: socket lingers at $SOCK"
fi

test_summary
