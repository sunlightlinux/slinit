#!/bin/sh
# 04-start-stop — deploy a long-running process service, start it, verify
# STARTED, stop it, verify STOPPED. Self-contained; cleans up its own service.

SVC="acceptance-test-longrun"

trap 'svc_remove "$SVC"' EXIT INT TERM

svc_deploy "$SVC" <<EOF
type = process
command = /bin/sh -c 'while true; do sleep 60; done'
restart = false
EOF

slinitctl --system start "$SVC" >/dev/null 2>&1
wait_for_service "$SVC" "STARTED" 10 || true
assert_service_state "$SVC" "STARTED" "$SVC reached STARTED"

# It must have a live PID.
_pid="$(slinitctl --system status "$SVC" 2>/dev/null | awk '/PID:/ {print $2; exit}')"
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -n "$_pid" ] && [ -d "/proc/$_pid" ]; then
    echo "OK: $SVC pid $_pid alive"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: $SVC has no live PID (got '$_pid')"
fi

slinitctl --system stop "$SVC" >/dev/null 2>&1
wait_for_service "$SVC" "STOPPED" 10 || true
assert_service_state "$SVC" "STOPPED" "$SVC reached STOPPED"

# After stop the previous PID must be gone.
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -n "$_pid" ] && [ ! -d "/proc/$_pid" ]; then
    echo "OK: $SVC pid $_pid is gone after stop"
elif [ -z "$_pid" ]; then
    echo "OK: skipping post-stop pid check (no pid was captured)"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: $SVC pid $_pid still alive after stop"
fi

test_summary
