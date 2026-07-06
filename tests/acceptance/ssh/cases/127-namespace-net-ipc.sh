#!/bin/sh
# 127-namespace-net + namespace-ipc — verifies the service runs in
# distinct network + IPC namespaces from PID 1.

SVC="${ACCEPTANCE_NS_PREFIX}netipc"

cleanup() {
    svc_remove "$SVC"
}
trap cleanup EXIT INT TERM
cleanup

svc_deploy "$SVC" <<EOF
type = process
namespace-net = yes
namespace-ipc = yes
command = /bin/sh -c 'while :; do sleep 60; done'
restart = no
EOF

slinitctl --system start "$SVC" 2>/dev/null
wait_for_service "$SVC" STARTED 10
assert_eq "$(svc_state "$SVC")" "STARTED" "service reached STARTED"

_pid=$(slinitctl --system status "$SVC" 2>/dev/null | awk '/^  PID:/ { print $2 }')
sleep 0.3

# /proc/<pid>/ns/{net,ipc} must differ from PID 1's inode ids.
_pid1_net=$(readlink "/proc/1/ns/net")
_svc_net=$(readlink "/proc/$_pid/ns/net")
_pid1_ipc=$(readlink "/proc/1/ns/ipc")
_svc_ipc=$(readlink "/proc/$_pid/ns/ipc")

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$_pid1_net" != "$_svc_net" ]; then
    echo "OK: net ns differs — pid1=$_pid1_net svc=$_svc_net"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: net ns identical: $_svc_net"
fi

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$_pid1_ipc" != "$_svc_ipc" ]; then
    echo "OK: ipc ns differs — pid1=$_pid1_ipc svc=$_svc_ipc"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: ipc ns identical: $_svc_ipc"
fi

test_summary
