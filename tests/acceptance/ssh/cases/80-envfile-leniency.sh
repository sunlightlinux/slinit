#!/bin/sh
# 80-envfile-leniency — system manager keeps booting if --env-file
# can't be read; user-mode slinit terminates.
#
# Dinit-parity (upstream bb2dea8 + 2883533): a system manager is doing
# critical bring-up; aborting because someone's env file is missing
# would brick a soft-reboot or a container start. User-mode slinit
# has no such constraint, so any env-file failure is a hard error.
#
# cmd/slinit/main.go handles this around the ReadEnvFile call —
# isPID1 || systemMode || containerMode → log + continue; otherwise
# os.Exit(1).
#
# A live PID-1 restart isn't safe on the test VM, so we drive it from
# nested slinit instances with -o (container mode = a system manager
# class) vs neither flag (user mode).

WORK="/tmp/acceptance-envfile"
SVCDIR="$WORK/svc.d"
SOCKET_M="$WORK/mgr.sock"
LOG_M="$WORK/mgr.log"
LOG_U="$WORK/user.log"
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

# --- Probe 1: system manager (container mode) keeps booting ---------
# /no/such/path/env doesn't exist; -o (system manager) must NOT
# terminate, the daemon should still open its control socket.
nohup slinit -o -m -p "$SOCKET_M" -d "$SVCDIR" -t boot \
    -e /no/such/path/env >"$LOG_M" 2>&1 &
MGR_PID=$!

_e=0
while [ "$_e" -lt 6 ]; do
    [ -S "$SOCKET_M" ] && break
    sleep 1
    _e=$((_e + 1))
done

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -S "$SOCKET_M" ]; then
    echo "OK: system manager kept booting despite missing env-file"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: system manager terminated. tail of log:"
    tail -15 "$LOG_M" 2>/dev/null | sed 's/^/  | /'
    test_summary
    exit 1
fi

# The Error line must be present so the operator still gets a paper
# trail of the problem.
_TESTS_RUN=$((_TESTS_RUN + 1))
if grep -qE "env-file.*(no such|missing|cannot|failed)" "$LOG_M" 2>/dev/null; then
    echo "OK: error logged for the missing env-file"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: no error line for the missing env-file:"
    sed 's/^/  | /' "$LOG_M"
fi

# --- Probe 2: user-mode slinit refuses to start with bad env-file ---
# -p socket but NO -o, NO -m: that's user-mode. Reading a missing
# env-file must terminate the daemon with a non-zero exit code.
slinit -p "$WORK/user.sock" -d "$SVCDIR" -t boot \
    -e /no/such/path/env >"$LOG_U" 2>&1
_rc=$?

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$_rc" -ne 0 ]; then
    echo "OK: user-mode slinit exited non-zero (rc=$_rc)"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: user-mode slinit returned 0 with a missing env-file"
fi

# The socket must NOT have appeared — the daemon never reached the
# Run loop.
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ ! -S "$WORK/user.sock" ]; then
    echo "OK: user-mode socket never opened (daemon bailed early)"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: user-mode socket appeared despite the bad env-file"
fi

test_summary
