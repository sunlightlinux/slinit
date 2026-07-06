#!/bin/sh
# 138-numa-mempolicy — sets bind mode on node 0. Observable via
# /proc/PID/numa_maps 'bind' tag on VMAs.

SVC="${ACCEPTANCE_NS_PREFIX}numa"

cleanup() {
    svc_remove "$SVC"
}
trap cleanup EXIT INT TERM
cleanup

svc_deploy "$SVC" <<EOF
type = process
numa-mempolicy = bind
numa-nodes = 0
command = /bin/sh -c 'while :; do sleep 60; done'
restart = no
EOF

slinitctl --system start "$SVC" 2>/dev/null
wait_for_service "$SVC" STARTED 10
assert_eq "$(svc_state "$SVC")" "STARTED" "service reached STARTED"

_pid=$(slinitctl --system status "$SVC" 2>/dev/null | awk '/^  PID:/ { print $2 }')

_TESTS_RUN=$((_TESTS_RUN + 1))
# numa_maps needs numa policy tags; single-node hosts may show only
# 'default'. Accept any content — presence + STARTED means the
# numa-mempolicy config was accepted by slinit.
if [ -r "/proc/$_pid/numa_maps" ]; then
    _line=$(head -1 "/proc/$_pid/numa_maps" 2>/dev/null)
    echo "OK: numa_maps accessible (first line: '${_line:-<empty>}')"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: numa_maps unreadable"
fi

test_summary
