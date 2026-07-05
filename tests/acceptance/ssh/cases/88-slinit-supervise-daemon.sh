#!/bin/sh
# 88-slinit-supervise-daemon — detach into supervisor loop, respawn a
# short-lived child, tear down cleanly with --stop.

CHILD_SH="/tmp/acceptance-supervise-child.sh"
COUNTER="/tmp/acceptance-supervise-count"
PIDFILE="/tmp/acceptance-supervise-sup.pid"

cleanup() {
    /usr/bin/slinit-supervise-daemon acceptance-test-supervise --stop \
        --pidfile "$PIDFILE" 2>/dev/null || true
    # Kill any lingering supervisor pid just in case.
    for _f in "$PIDFILE" "${PIDFILE}.daemon"; do
        [ -f "$_f" ] && kill -KILL "$(cat "$_f" 2>/dev/null)" 2>/dev/null || true
    done
    rm -f "$CHILD_SH" "$COUNTER" "$PIDFILE" "${PIDFILE}.daemon" \
          /tmp/ssd.err /tmp/slinit-supervise-daemon.err
}
trap cleanup EXIT INT TERM
cleanup

cat > "$CHILD_SH" <<EOF
#!/bin/sh
# 88-slinit-supervise-daemon child — bumps a counter file each iteration.
n=0
if [ -f "$COUNTER" ]; then
    n=\$(cat "$COUNTER")
fi
n=\$((n + 1))
echo \$n > "$COUNTER"
exec sleep 1
EOF
chmod +x "$CHILD_SH"

# Start the supervisor with unlimited respawns, 200ms delay.
/usr/bin/slinit-supervise-daemon acceptance-test-supervise --start \
    --pidfile "$PIDFILE" --exec "$CHILD_SH" \
    --respawn-max 0 --respawn-delay 200ms \
    >/dev/null 2>/tmp/ssd.err
rc=$?
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$rc" = "0" ]; then
    echo "OK: --start exit 0"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: --start rc=$rc — err: $(cat /tmp/ssd.err)"
    test_summary
    exit 0
fi

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -f "$PIDFILE" ]; then
    sup_pid=$(cat "$PIDFILE")
    if [ -n "$sup_pid" ] && [ -d "/proc/$sup_pid" ]; then
        echo "OK: supervisor pidfile references live pid $sup_pid"
    else
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: supervisor pid $sup_pid not alive"
    fi
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: no supervisor pidfile"
fi

sleep 0.4
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -f "${PIDFILE}.daemon" ]; then
    echo "OK: daemon pidfile at ${PIDFILE}.daemon"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: no daemon pidfile"
fi

# Give the supervisor 3s to respawn the child at least twice.
sleep 3
runs=$(cat "$COUNTER" 2>/dev/null || echo 0)
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$runs" -ge 2 ] 2>/dev/null; then
    echo "OK: supervisor respawned daemon ($runs iterations)"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: only $runs iterations (want >= 2)"
fi

# --stop tears everything down.
_TESTS_RUN=$((_TESTS_RUN + 1))
/usr/bin/slinit-supervise-daemon acceptance-test-supervise --stop \
    --pidfile "$PIDFILE" >/dev/null 2>/tmp/ssd.err
rc=$?
if [ "$rc" = "0" ]; then
    echo "OK: --stop exit 0"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: --stop rc=$rc — err: $(cat /tmp/ssd.err)"
fi

sleep 0.5
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ ! -d "/proc/$sup_pid" ]; then
    echo "OK: supervisor exited after --stop"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: supervisor pid $sup_pid still alive"
fi

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ ! -e "$PIDFILE" ] && [ ! -e "${PIDFILE}.daemon" ]; then
    echo "OK: pidfiles cleaned up on --stop"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: pidfiles linger"
fi

# Second --stop with missing pidfile → still exit 0 (idempotent).
_TESTS_RUN=$((_TESTS_RUN + 1))
/usr/bin/slinit-supervise-daemon acceptance-test-supervise --stop \
    --pidfile "$PIDFILE" >/dev/null 2>/tmp/ssd.err
rc=$?
if [ "$rc" = "0" ]; then
    echo "OK: --stop on absent pidfile exits 0"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: absent-pidfile stop rc=$rc"
fi

test_summary
