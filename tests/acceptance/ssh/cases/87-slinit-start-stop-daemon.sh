#!/bin/sh
# 87-slinit-start-stop-daemon — Debian/OpenRC start-stop-daemon(8)
# spawn + stop lifecycle against /bin/sleep.
#
# Combines --name (comm-based match) with --startas (spawn-only) so
# /proc/PID/exe pointing at busybox on Alpine/Void doesn't defeat
# matching. Same pattern real init.d scripts use.

PIDFILE="/tmp/acceptance-ssd-sleeper.pid"
SLEEP=$(command -v sleep)
cleanup() {
    if [ -f "$PIDFILE" ]; then
        pid=$(cat "$PIDFILE" 2>/dev/null)
        [ -n "$pid" ] && kill -KILL "$pid" 2>/dev/null || true
        rm -f "$PIDFILE"
    fi
    rm -f /tmp/ssd.out /tmp/ssd.err
}
trap cleanup EXIT INT TERM
cleanup

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -x "$SLEEP" ]; then
    echo "OK: found sleep at $SLEEP"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: no sleep binary in PATH"
    test_summary
    exit 0
fi

# --start --background --make-pidfile
/usr/bin/slinit-start-stop-daemon --start --background --make-pidfile \
    --pidfile "$PIDFILE" --name sleep --startas "$SLEEP" -- 300
sleep 0.3

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -f "$PIDFILE" ]; then
    echo "OK: --make-pidfile wrote $PIDFILE"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: no pidfile after --start"
fi

pid=$(cat "$PIDFILE" 2>/dev/null)
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -n "$pid" ] && [ -d "/proc/$pid" ]; then
    echo "OK: pidfile references live pid ($pid)"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: pid $pid not alive"
fi

# --status probe.
_TESTS_RUN=$((_TESTS_RUN + 1))
/usr/bin/slinit-start-stop-daemon --status --pidfile "$PIDFILE" >/dev/null 2>&1
rc=$?
if [ "$rc" = "0" ]; then
    echo "OK: --status exits 0 while running"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: --status rc=$rc"
fi

# Double --start should refuse (exit 1); --oknodo softens to 0.
_TESTS_RUN=$((_TESTS_RUN + 1))
/usr/bin/slinit-start-stop-daemon --start --background --make-pidfile \
    --pidfile "$PIDFILE" --name sleep --startas "$SLEEP" -- 300 \
    >/tmp/ssd.out 2>/tmp/ssd.err
rc=$?
if [ "$rc" = "1" ]; then
    echo "OK: double --start refused with exit 1"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: rc=$rc, want 1"
fi

_TESTS_RUN=$((_TESTS_RUN + 1))
/usr/bin/slinit-start-stop-daemon --start --background --make-pidfile --oknodo \
    --pidfile "$PIDFILE" --name sleep --startas "$SLEEP" -- 300 \
    >/tmp/ssd.out 2>/tmp/ssd.err
rc=$?
if [ "$rc" = "0" ]; then
    echo "OK: --oknodo softens double-start to exit 0"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: --oknodo rc=$rc"
fi

# --stop --retry TERM/2/KILL/2.
_TESTS_RUN=$((_TESTS_RUN + 1))
/usr/bin/slinit-start-stop-daemon --stop --pidfile "$PIDFILE" \
    --retry TERM/2/KILL/2 >/tmp/ssd.out 2>/tmp/ssd.err
rc=$?
if [ "$rc" = "0" ]; then
    echo "OK: --stop exits 0"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: --stop rc=$rc"
fi

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -d "/proc/$pid" ] &&
   [ "$(cat /proc/$pid/status 2>/dev/null | grep '^State' | awk '{print $2}')" != "Z" ]; then
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: pid $pid still alive after --stop"
else
    echo "OK: pid $pid is gone or zombie"
fi

# Stale pidfile → LSB code 5; --oknodo softens.
echo "999999" > "$PIDFILE"
_TESTS_RUN=$((_TESTS_RUN + 1))
/usr/bin/slinit-start-stop-daemon --stop --pidfile "$PIDFILE" \
    >/tmp/ssd.out 2>/tmp/ssd.err
rc=$?
if [ "$rc" = "5" ]; then
    echo "OK: stale pidfile yields LSB code 5"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: stale rc=$rc, want 5"
fi

echo "999999" > "$PIDFILE"
_TESTS_RUN=$((_TESTS_RUN + 1))
/usr/bin/slinit-start-stop-daemon --stop --pidfile "$PIDFILE" --oknodo \
    >/tmp/ssd.out 2>/tmp/ssd.err
rc=$?
if [ "$rc" = "0" ]; then
    echo "OK: --oknodo softens stale-pidfile stop to 0"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: --oknodo stale rc=$rc"
fi

test_summary
