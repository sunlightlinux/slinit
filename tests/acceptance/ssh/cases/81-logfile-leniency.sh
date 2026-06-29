#!/bin/sh
# 81-logfile-leniency — system manager keeps booting if --log-file
# can't be opened; user-mode slinit terminates.
#
# Dinit-parity (upstream 3e48a8e): "bail out if a specified logfile
# can't be opened" — the check now targets the system-manager role
# rather than just system init (PID 1). A long-running container or
# system-manager invocation must not crash mid-boot over a logfile
# path the operator typoed; it logs to stderr/console and presses on.
# A user-mode invocation has no such constraint, so the typo is fatal.
#
# Both modes share the same code path in cmd/slinit/main.go around the
# os.OpenFile(logFile, ...) call.

WORK="/tmp/acceptance-logfile"
SVCDIR="$WORK/svc.d"
SOCKET_M="$WORK/mgr.sock"
LOG_M="$WORK/mgr.log"
LOG_U="$WORK/user.log"
BAD_LOGFILE="/no/such/path/log.txt"
MGR_PID=""

cleanup() {
    if [ -n "$MGR_PID" ]; then
        kill -TERM "$MGR_PID" 2>/dev/null
        for _ in 1 2 3; do
            kill -0 "$MGR_PID" 2>/dev/null || break
            sleep 1
        done
        kill -KILL "$MGR_PID" 2>/dev/null
    fi
    rm -rf "$WORK"
}
trap cleanup EXIT INT TERM
cleanup
mkdir -p "$SVCDIR"

cat > "$SVCDIR/boot" <<EOF
type = internal
EOF

# --- Probe 1: system manager keeps booting ---------------------------
# -o (container = system manager class) with an unopenable --log-file
# must NOT terminate. Daemon must still open its control socket.
nohup slinit -o -m -p "$SOCKET_M" -d "$SVCDIR" -t boot \
    -l "$BAD_LOGFILE" >"$LOG_M" 2>&1 &
MGR_PID=$!

_e=0
while [ "$_e" -lt 6 ]; do
    [ -S "$SOCKET_M" ] && break
    sleep 1
    _e=$((_e + 1))
done

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -S "$SOCKET_M" ]; then
    echo "OK: system manager kept booting despite unopenable log-file"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: system manager terminated. tail of stderr:"
    tail -10 "$LOG_M" 2>/dev/null | sed 's/^/  | /'
    test_summary
    exit 1
fi

# Operator must still see the diagnostic on stderr (= our $LOG_M).
_TESTS_RUN=$((_TESTS_RUN + 1))
if grep -qE "cannot open log file" "$LOG_M" 2>/dev/null; then
    echo "OK: error logged for the unopenable log-file"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: 'cannot open log file' missing from stderr:"
    sed 's/^/  | /' "$LOG_M"
fi

# --- Probe 2: user-mode slinit refuses to start ---------------------
slinit -p "$WORK/user.sock" -d "$SVCDIR" -t boot \
    -l "$BAD_LOGFILE" >"$LOG_U" 2>&1
_rc=$?

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$_rc" -ne 0 ]; then
    echo "OK: user-mode slinit exited non-zero (rc=$_rc)"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: user-mode slinit returned 0 with a bad log-file"
fi

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ ! -S "$WORK/user.sock" ]; then
    echo "OK: user-mode socket never opened (daemon bailed early)"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: user-mode socket appeared despite the bad log-file"
fi

test_summary
