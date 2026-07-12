#!/bin/sh
# Test: mlockall = current+future locks pages so /proc/PID/status
# VmLck > 0.

SVC="test-mlk"

cat > "/etc/slinit.d/$SVC" <<EOF
type = process
mlockall = current+future
command = /bin/sh -c 'while :; do sleep 60; done'
restart = no
EOF

slinitctl --system start "$SVC" 2>/dev/null
wait_for_service "$SVC" STARTED 10
assert_service_state "$SVC" "STARTED" "service reached STARTED"

_pid=""
_i=0
while [ "$_i" -lt 5 ]; do
    _pid=$(slinitctl --system status "$SVC" 2>/dev/null | awk '/PID:/ { print $2; exit }')
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
