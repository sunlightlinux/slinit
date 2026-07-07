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

# PID may briefly be absent from status output right after STARTED
# reports terminal — poll a couple of times before giving up so a
# late-suite SSH round-trip doesn't paper over a real leak. VmLck
# populates within microseconds of the mlockall() syscall in the
# child; retry only needed for the PID column.
_pid=""
_i=0
while [ "$_i" -lt 5 ]; do
    _pid=$(slinitctl --system status "$SVC" 2>/dev/null | awk '/^  PID:/ { print $2 }')
    [ -n "$_pid" ] && [ "$_pid" != "0" ] && break
    sleep 0.2
    _i=$((_i + 1))
done
_vmlck=$(awk '/^VmLck:/ { print $2 }' "/proc/$_pid/status" 2>/dev/null)

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -n "$_vmlck" ] && [ "$_vmlck" != "0" ]; then
    echo "OK: VmLck=$_vmlck kB (>0)"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: VmLck='$_vmlck' — expected > 0"
fi

test_summary
