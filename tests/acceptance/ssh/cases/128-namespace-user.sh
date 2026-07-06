#!/bin/sh
# 128-namespace-user — service gets its own user namespace.

SVC="${ACCEPTANCE_NS_PREFIX}usrns"

cleanup() {
    svc_remove "$SVC"
}
trap cleanup EXIT INT TERM
cleanup

svc_deploy "$SVC" <<EOF
type = process
namespace-user = yes
command = /bin/sh -c 'while :; do sleep 60; done'
restart = no
EOF

slinitctl --system start "$SVC" 2>/dev/null
wait_for_service "$SVC" STARTED 10
assert_eq "$(svc_state "$SVC")" "STARTED" "service reached STARTED"

_pid=$(slinitctl --system status "$SVC" 2>/dev/null | awk '/^  PID:/ { print $2 }')
sleep 0.3

_pid1_user=$(readlink "/proc/1/ns/user")
_svc_user=$(readlink "/proc/$_pid/ns/user")

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$_pid1_user" != "$_svc_user" ]; then
    echo "OK: user ns differs — pid1=$_pid1_user svc=$_svc_user"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: user ns identical: $_svc_user"
fi

test_summary
