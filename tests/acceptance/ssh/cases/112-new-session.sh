#!/bin/sh
# 112-new-session — `new-session=yes` calls setsid(2) before exec so
# the service leads its own session/process group.

SVC="${ACCEPTANCE_NS_PREFIX}newsess"

cleanup() {
    svc_remove "$SVC"
}
trap cleanup EXIT INT TERM
cleanup

svc_deploy "$SVC" <<EOF
type = process
command = /bin/sh -c 'while true; do sleep 60; done'
new-session = yes
restart = no
EOF

slinitctl --system start "$SVC" 2>/dev/null
wait_for_service "$SVC" STARTED 10
assert_eq "$(svc_state "$SVC")" "STARTED" "service reached STARTED"

_pid=$(slinitctl --system status "$SVC" 2>/dev/null | awk '/^  PID:/ { print $2 }')

# Session leader: /proc/PID/stat's `session` field (col 6) equals PID
# when the process is the session leader.
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -n "$_pid" ] && [ -f "/proc/$_pid/stat" ]; then
    _sess=$(awk '{print $6}' "/proc/$_pid/stat" 2>/dev/null)
    if [ "$_sess" = "$_pid" ]; then
        echo "OK: pid $_pid is session leader (session field = $_sess)"
    else
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: pid=$_pid session=$_sess (want equal)"
    fi
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: could not resolve pid"
fi

test_summary
