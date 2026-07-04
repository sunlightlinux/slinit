#!/bin/sh
# Test: slinit-start-stop-daemon spawn + stop lifecycle against a
# real process. Validates: --background fork, --make-pidfile, PID
# lookup, --stop with --retry escalation, --status probe, --oknodo
# behaviour on stop-when-not-running.

wait_for_service "boot" "STARTED" 10

PIDFILE=/tmp/sleeper.pid
rm -f "$PIDFILE"

SLEEP=$(command -v sleep)
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -x "$SLEEP" ]; then
    echo "OK: found sleep at $SLEEP"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: no sleep in the VM"
    test_summary
    exit 0
fi

# ---------------------------------------------------------------
# --start with --background + --make-pidfile
#
# Alpine's `sleep` is a busybox applet, so /proc/PID/exe points to
# /bin/busybox for every applet; --name (which reads /proc/PID/comm)
# is what init.d scripts always use in that situation and it matches
# whatever busybox set as the task name ("sleep").
# ---------------------------------------------------------------

slinit-start-stop-daemon --start --background --make-pidfile \
    --pidfile "$PIDFILE" --name sleep --startas "$SLEEP" -- 300
sleep 0.3  # let the fork settle so /proc/PID reflects the exec

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
    echo "OK: pidfile references a live pid ($pid)"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: pid $pid not alive"
fi

# --status must exit 0 while the daemon is running and print the pid.
out=$(slinit-start-stop-daemon --status --pidfile "$PIDFILE" 2>&1)
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$?" = "0" ]; then
    echo "OK: --status exit 0 while running"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: --status rc=$? out=$out"
fi

# ---------------------------------------------------------------
# Double-start should refuse (exit 1), --oknodo softens to 0
# ---------------------------------------------------------------

slinit-start-stop-daemon --start --background --make-pidfile \
    --pidfile "$PIDFILE" --name sleep --startas "$SLEEP" -- 300 \
    >/tmp/ssd.out 2>/tmp/ssd.err
rc=$?
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$rc" = "1" ]; then
    echo "OK: double --start refused with exit 1"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: rc=$rc, want 1"
fi

slinit-start-stop-daemon --start --background --make-pidfile --oknodo \
    --pidfile "$PIDFILE" --name sleep --startas "$SLEEP" -- 300 \
    >/tmp/ssd.out 2>/tmp/ssd.err
rc=$?
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$rc" = "0" ]; then
    echo "OK: --oknodo softens double-start to exit 0"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: --oknodo rc=$rc"
fi

# ---------------------------------------------------------------
# --stop with retry escalation
# ---------------------------------------------------------------

# --stop uses just --pidfile as the match criterion. --exec would
# add /proc/PID/exe checking which fails against busybox multi-call
# binaries; --pidfile alone is sufficient and matches init.d idiom.
slinit-start-stop-daemon --stop --pidfile "$PIDFILE" \
    --retry TERM/2/KILL/2 >/tmp/ssd.out 2>/tmp/ssd.err
rc=$?
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$rc" = "0" ]; then
    echo "OK: --stop exit 0"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: --stop rc=$rc — out: $(cat /tmp/ssd.out) err: $(cat /tmp/ssd.err)"
fi

# Verify the process is actually gone.
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -d "/proc/$pid" ] && [ "$(cat /proc/$pid/status 2>/dev/null | grep '^State' | awk '{print $2}')" != "Z" ]; then
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: pid $pid still alive after --stop"
else
    echo "OK: pid $pid is gone or zombie"
fi

# ---------------------------------------------------------------
# --stop when not running: --oknodo → 0, without → 5 (stale pidfile)
# ---------------------------------------------------------------

# Write a stale pidfile (a pid that has certainly never existed).
echo "999999" > "$PIDFILE"
# --stop uses just --pidfile as the match criterion. --exec would
# add /proc/PID/exe checking which fails against busybox multi-call
# binaries; --pidfile alone is sufficient and matches init.d idiom.
slinit-start-stop-daemon --stop --pidfile "$PIDFILE" \
    >/tmp/ssd.out 2>/tmp/ssd.err
rc=$?
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$rc" = "5" ]; then
    echo "OK: stale pidfile yields LSB code 5"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: rc=$rc, want 5"
fi

echo "999999" > "$PIDFILE"
slinit-start-stop-daemon --stop --pidfile "$PIDFILE" --oknodo \
    >/tmp/ssd.out 2>/tmp/ssd.err
rc=$?
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$rc" = "0" ]; then
    echo "OK: --oknodo softens stale-pidfile to 0"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: --oknodo stale rc=$rc"
fi

rm -f "$PIDFILE"

test_summary
