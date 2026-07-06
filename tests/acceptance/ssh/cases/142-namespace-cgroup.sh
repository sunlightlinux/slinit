#!/bin/sh
# 142-namespace-cgroup — service runs in its own cgroup namespace.

SVC="${ACCEPTANCE_NS_PREFIX}cgns"

cleanup() {
    svc_remove "$SVC"
}
trap cleanup EXIT INT TERM
cleanup

svc_deploy "$SVC" <<EOF
type = process
namespace-cgroup = yes
command = /bin/sh -c 'while :; do sleep 60; done'
restart = no
EOF

slinitctl --system start "$SVC" 2>/dev/null
wait_for_service "$SVC" STARTED 10
assert_eq "$(svc_state "$SVC")" "STARTED" "service reached STARTED"

_pid=$(slinitctl --system status "$SVC" 2>/dev/null | awk '/^  PID:/ { print $2 }')
_pid1_cg=$(readlink "/proc/1/ns/cgroup")
_svc_cg=$(readlink "/proc/$_pid/ns/cgroup")

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$_pid1_cg" != "$_svc_cg" ]; then
    echo "OK: cgroup ns differs — pid1=$_pid1_cg svc=$_svc_cg"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: cgroup ns identical: $_svc_cg"
fi

test_summary
