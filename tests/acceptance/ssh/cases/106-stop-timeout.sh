#!/bin/sh
# 106-stop-timeout — SIGKILL escalation when a service ignores SIGTERM
# past stop-timeout seconds.

SVC="${ACCEPTANCE_NS_PREFIX}stoptmo"

cleanup() {
    svc_remove "$SVC"
}
trap cleanup EXIT INT TERM
cleanup

# Deliberately ignore SIGTERM in the shell so slinit has to escalate
# to SIGKILL after stop-timeout=3 seconds.
svc_deploy "$SVC" <<EOF
type = process
command = /bin/sh -c 'trap "" TERM; while true; do sleep 60; done'
stop-timeout = 3
restart = no
EOF

slinitctl --system start "$SVC" 2>/dev/null
wait_for_service "$SVC" STARTED 10
assert_eq "$(svc_state "$SVC")" "STARTED" "service reached STARTED"

_pid=$(slinitctl --system status "$SVC" 2>/dev/null | awk '/^  PID:/ { print $2 }')
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -n "$_pid" ] && [ -d "/proc/$_pid" ]; then
    echo "OK: located service pid $_pid"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: could not resolve pid"
    test_summary
    exit 0
fi

# Fire an async stop (should send SIGTERM immediately, SIGKILL after 3s).
_start=$(date +%s)
slinitctl --system stop "$SVC" 2>/dev/null &
_stop_pid=$!

# Wait up to 8s for the service to actually die.
_i=0
while [ "$_i" -lt 16 ] && [ -d "/proc/$_pid" ]; do
    _i=$((_i + 1))
    sleep 0.5
done
wait "$_stop_pid" 2>/dev/null || true
_elapsed=$(( $(date +%s) - _start ))

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ ! -d "/proc/$_pid" ] || [ "$(awk '{print $3}' /proc/$_pid/stat 2>/dev/null)" = "Z" ]; then
    echo "OK: pid $_pid is gone or zombie after stop"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: pid $_pid still alive"
fi

# The SIGKILL should have arrived within stop-timeout + a bit of
# slack — well before our 8s poll ceiling. If it takes longer, the
# timeout wasn't honored.
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$_elapsed" -le 6 ]; then
    echo "OK: escalation completed in ${_elapsed}s (<= 6s budget)"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: escalation took ${_elapsed}s"
fi

assert_eq "$(svc_state "$SVC")" "STOPPED" "service reached STOPPED"

test_summary
