#!/bin/sh
# Test: namespace-cgroup = yes puts the service in its own cgroup ns.
#
# The service's ns link must be non-empty. An empty _pid made readlink
# print nothing, and "" differs from PID 1's link, so the comparison
# passed with no service to compare.

SVC="test-cgns"

cat > "/etc/slinit.d/$SVC" <<EOF
type = process
namespace-cgroup = yes
command = /bin/sh -c 'while :; do sleep 60; done'
restart = no
EOF

slinitctl --system start "$SVC" 2>/dev/null
wait_for_service "$SVC" STARTED 10
assert_service_state "$SVC" "STARTED" "service reached STARTED"

_pid=$(slinitctl --system status "$SVC" 2>/dev/null | awk '/PID:/ { print $2; exit }')
_pid1_cg=$(readlink "/proc/1/ns/cgroup")
_svc_cg=$(readlink "/proc/$_pid/ns/cgroup")

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -n "$_pid" ] && [ -n "$_svc_cg" ]; then
    echo "OK: service pid=$_pid has a readable cgroup ns link"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: no cgroup ns link to compare — pid='$_pid' link='$_svc_cg'"
fi

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -n "$_svc_cg" ] && [ "$_pid1_cg" != "$_svc_cg" ]; then
    echo "OK: cgroup ns differs — pid1=$_pid1_cg svc=$_svc_cg"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: cgroup ns identical: $_svc_cg"
fi

test_summary
