#!/bin/sh
# 102-debug-flag — `debug = yes` SIGSTOPs slinit-runner before exec.
# The operator attaches, then SIGCONTs to let the service actually
# run. This case verifies both halves: the child stays stopped until
# CONT, then the real command executes.

SVC="${ACCEPTANCE_NS_PREFIX}debug"
MARKER="/tmp/acceptance-debug-marker"

cleanup() {
    # If the case aborts mid-flight, SIGCONT any lingering stopped
    # runner so the service can drain and stop cleanly.
    for _pid in $(pgrep -f "slinit-runner.*debug" 2>/dev/null); do
        kill -CONT "$_pid" 2>/dev/null || true
    done
    svc_remove "$SVC"
    rm -f "$MARKER"
}
trap cleanup EXIT INT TERM
cleanup

svc_deploy "$SVC" <<EOF
type = process
command = /bin/sh -c 'echo running > $MARKER; while true; do sleep 60; done'
debug = yes
restart = no
EOF

slinitctl --system start "$SVC" 2>/dev/null
# Debug=yes leaves the runner in STOPPED (T) state; slinit reports
# STARTING while waiting.
sleep 2

# Marker must NOT exist yet — the service body has not run.
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ ! -e "$MARKER" ]; then
    echo "OK: service body suppressed while runner is SIGSTOP'd"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: marker exists too early — $(cat $MARKER)"
fi

# Locate the runner process and verify /proc/PID/stat shows it in
# 'T' (stopped) state.
_pid=$(pgrep -f "slinit-runner.*debug" | head -1)
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -n "$_pid" ]; then
    echo "OK: found stopped slinit-runner (pid $_pid)"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: could not find slinit-runner --debug"
    test_summary
    exit 0
fi

_TESTS_RUN=$((_TESTS_RUN + 1))
_state=$(awk '{print $3}' "/proc/$_pid/stat" 2>/dev/null)
if [ "$_state" = "T" ]; then
    echo "OK: runner is in stopped state (T)"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: runner state = '$_state' (want T)"
fi

# Resume: the runner now exec's into the real command.
kill -CONT "$_pid"

# Poll for the marker to appear.
_i=0
while [ "$_i" -lt 20 ]; do
    [ "$(cat "$MARKER" 2>/dev/null)" = "running" ] && break
    _i=$((_i + 1))
    sleep 0.5
done

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$(cat "$MARKER" 2>/dev/null)" = "running" ]; then
    echo "OK: service ran after SIGCONT"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: service did not run after SIGCONT"
fi

test_summary
