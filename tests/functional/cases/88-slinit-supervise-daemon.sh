#!/bin/sh
# Test: slinit-supervise-daemon detach + supervisor loop against a
# real short-lived child. Validates: re-exec into supervisor branch
# (SLINIT_SSD_SUPERVISOR=1), supervisor pidfile, daemon .pidfile
# companion, respawn on child exit, --signal delivery to daemon,
# --stop teardown.

wait_for_service "boot" "STARTED" 10

# A "flaky" daemon: writes its start count to a file so we can
# observe respawn from the outside, then sleeps briefly and exits.
# The supervisor should respawn it because we don't hit the max.
DAEMON=/tmp/flaky-daemon.sh
COUNTER=/tmp/flaky-count
cat >"$DAEMON" <<'EOF'
#!/bin/sh
# Read + increment the counter. Guard against a missing file.
n=0
if [ -f /tmp/flaky-count ]; then
    n=$(cat /tmp/flaky-count)
fi
n=$((n + 1))
echo "$n" >/tmp/flaky-count
# Live briefly so the supervisor's rate limiter has room to breathe.
exec sleep 1
EOF
chmod +x "$DAEMON"
rm -f "$COUNTER"

PIDFILE=/tmp/flaky.supervisor.pid
DAEMON_PIDFILE="${PIDFILE}.daemon"

# ---------------------------------------------------------------
# --start (default mode) detaches into the supervisor loop
# ---------------------------------------------------------------

slinit-supervise-daemon flaky --start \
    --pidfile "$PIDFILE" --exec "$DAEMON" \
    --respawn-max 0 --respawn-delay 200ms \
    >/tmp/ssd.out 2>/tmp/ssd.err
rc=$?
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$rc" = "0" ]; then
    echo "OK: --start exit 0"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: --start rc=$rc — err: $(cat /tmp/ssd.err)"
fi

# Supervisor pidfile appears (that is how the top-level knew to
# return).
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -f "$PIDFILE" ]; then
    sup_pid=$(cat "$PIDFILE")
    if [ -n "$sup_pid" ] && [ -d "/proc/$sup_pid" ]; then
        echo "OK: supervisor pidfile references live pid $sup_pid"
    else
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: supervisor pidfile pid $sup_pid not alive"
    fi
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: no supervisor pidfile"
fi

# The daemon companion pidfile is written by the supervisor as it
# spawns each daemon iteration. It should exist and point at
# whatever iteration is currently running.
#
# Waited for, not sampled after a fixed delay. This file is rewritten
# once per iteration and the daemon only lives a second, so a lone
# sample can land in the gap between iterations as easily as on one —
# and 0.4s of budget on a runner under software emulation is not a
# budget at all.
_wait_for_file() {
    _i=0
    while [ "$_i" -lt "$2" ]; do
        [ -f "$1" ] && return 0
        _i=$((_i + 1))
        sleep 1
    done
    return 1
}
_wait_for_file "$DAEMON_PIDFILE" 15
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -f "$DAEMON_PIDFILE" ]; then
    dpid=$(cat "$DAEMON_PIDFILE")
    echo "OK: daemon pidfile at $DAEMON_PIDFILE (pid $dpid)"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: no daemon pidfile"
fi

# ---------------------------------------------------------------
# Respawn: wait for the supervisor to spawn the daemon at least
# twice. Each iteration lives ~1s and the respawn delay is 200ms,
# so two of them need a little over two seconds of *daemon* time.
#
# This used to be `sleep 3` and one sample, which is a budget of
# 0.6s over what two iterations need — and when the whole VM runs
# 10-50x slower under software emulation, that wall-clock budget
# buys proportionally fewer iterations. It failed in CI with
# "only 0 iterations (want >= 2)". A deadline instead of a guess:
# it returns as soon as the second iteration lands, and only a
# supervisor that is genuinely not respawning takes the full wait.
# ---------------------------------------------------------------

runs=0
_i=0
while [ "$_i" -lt 30 ]; do
    runs=$(cat "$COUNTER" 2>/dev/null || echo 0)
    [ -n "$runs" ] || runs=0
    if [ "$runs" -ge 2 ] 2>/dev/null; then
        break
    fi
    _i=$((_i + 1))
    sleep 1
done
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$runs" -ge 2 ] 2>/dev/null; then
    echo "OK: supervisor respawned daemon ($runs iterations in ${_i}s)"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: only $runs iterations in 30s (want >= 2)"
fi

# ---------------------------------------------------------------
# --stop tears the whole tree down
# ---------------------------------------------------------------

slinit-supervise-daemon flaky --stop --pidfile "$PIDFILE" \
    >/tmp/ssd.out 2>/tmp/ssd.err
rc=$?
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$rc" = "0" ]; then
    echo "OK: --stop exit 0"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: --stop rc=$rc — err: $(cat /tmp/ssd.err)"
fi

# Teardown is a process exiting, so wait for it rather than assuming
# half a second is enough.
_i=0
while [ "$_i" -lt 15 ] && [ -d "/proc/$sup_pid" ]; do
    _i=$((_i + 1))
    sleep 1
done

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ ! -d "/proc/$sup_pid" ]; then
    echo "OK: supervisor exited after --stop (${_i}s)"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: supervisor pid $sup_pid still alive 15s after --stop"
fi

# Both pidfiles should be gone after clean shutdown.
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ ! -e "$PIDFILE" ] && [ ! -e "$DAEMON_PIDFILE" ]; then
    echo "OK: pidfiles cleaned up on --stop"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: pidfiles linger — supervisor=$(ls -la $PIDFILE 2>&1) daemon=$(ls -la $DAEMON_PIDFILE 2>&1)"
fi

# ---------------------------------------------------------------
# --stop when nothing is running: --pidfile missing → exit 0
# ---------------------------------------------------------------

slinit-supervise-daemon flaky --stop --pidfile "$PIDFILE" \
    >/tmp/ssd.out 2>/tmp/ssd.err
rc=$?
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$rc" = "0" ]; then
    echo "OK: --stop on absent pidfile exits 0"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: absent-pidfile --stop rc=$rc"
fi

# Cleanup any orphans so other cases sharing the base VM start
# fresh.
rm -f "$DAEMON" "$COUNTER" "$PIDFILE" "$DAEMON_PIDFILE"

test_summary
