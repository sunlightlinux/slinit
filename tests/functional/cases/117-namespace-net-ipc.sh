#!/bin/sh
# Test: namespace-net + namespace-ipc — service runs in distinct
# network + IPC namespaces from PID 1.
#
# The service's own ns links must be non-empty. An empty _pid made
# readlink print nothing, and "" differs from PID 1's link, so a service
# that never came up (or a status output the awk missed) read as
# "namespace differs" — the comparison passed on no data at all.

SVC="test-netipc"

cat > "/etc/slinit.d/$SVC" <<EOF
type = process
namespace-net = yes
namespace-ipc = yes
command = /bin/sh -c 'while :; do sleep 60; done'
restart = no
EOF

slinitctl --system start "$SVC" 2>/dev/null
wait_for_service "$SVC" STARTED 10
assert_service_state "$SVC" "STARTED" "service reached STARTED"

_pid=$(slinitctl --system status "$SVC" 2>/dev/null | awk '/PID:/ { print $2; exit }')
sleep 0.3

_pid1_net=$(readlink "/proc/1/ns/net")
_svc_net=$(readlink "/proc/$_pid/ns/net")
_pid1_ipc=$(readlink "/proc/1/ns/ipc")
_svc_ipc=$(readlink "/proc/$_pid/ns/ipc")

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -n "$_pid" ] && [ -n "$_svc_net" ] && [ -n "$_svc_ipc" ]; then
    echo "OK: service pid=$_pid has readable ns links"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: no ns links to compare — pid='$_pid' net='$_svc_net' ipc='$_svc_ipc'"
fi

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -n "$_svc_net" ] && [ "$_pid1_net" != "$_svc_net" ]; then
    echo "OK: net ns differs — pid1=$_pid1_net svc=$_svc_net"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: net ns identical: $_svc_net"
fi

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -n "$_svc_ipc" ] && [ "$_pid1_ipc" != "$_svc_ipc" ]; then
    echo "OK: ipc ns differs — pid1=$_pid1_ipc svc=$_svc_ipc"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: ipc ns identical: $_svc_ipc"
fi

test_summary
