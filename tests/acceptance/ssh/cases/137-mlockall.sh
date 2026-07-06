#!/bin/sh
# 137-mlockall — with current+future, /proc/PID/status VmLck should
# be > 0.

SVC="${ACCEPTANCE_NS_PREFIX}mlk"

cleanup() {
    svc_remove "$SVC"
}
trap cleanup EXIT INT TERM
cleanup

svc_deploy "$SVC" <<EOF
type = process
mlockall = current+future
command = /bin/sh -c 'while :; do sleep 60; done'
restart = no
EOF

slinitctl --system start "$SVC" 2>/dev/null
wait_for_service "$SVC" STARTED 10
assert_eq "$(svc_state "$SVC")" "STARTED" "service reached STARTED"

_pid=$(slinitctl --system status "$SVC" 2>/dev/null | awk '/^  PID:/ { print $2 }')
_vmlck=$(awk '/^VmLck:/ { print $2 }' "/proc/$_pid/status" 2>/dev/null)

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -n "$_vmlck" ] && [ "$_vmlck" != "0" ]; then
    echo "OK: VmLck=$_vmlck kB (>0)"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: VmLck='$_vmlck' — expected > 0"
fi

test_summary
