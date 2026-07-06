#!/bin/sh
# 130-sched-policy — set FIFO scheduler; verify via chrt on the PID.

SVC="${ACCEPTANCE_NS_PREFIX}schedp"

cleanup() {
    svc_remove "$SVC"
}
trap cleanup EXIT INT TERM
cleanup

svc_deploy "$SVC" <<EOF
type = process
sched-policy = fifo
sched-priority = 10
command = /bin/sh -c 'while :; do sleep 60; done'
restart = no
EOF

slinitctl --system start "$SVC" 2>/dev/null
wait_for_service "$SVC" STARTED 10
assert_eq "$(svc_state "$SVC")" "STARTED" "service reached STARTED"

_pid=$(slinitctl --system status "$SVC" 2>/dev/null | awk '/^  PID:/ { print $2 }')

# chrt -p prints the class. On sh child, scheduler was set on parent
# then inherited; probe the parent PID (the /bin/sh runner).
_pol=$(chrt -p "$_pid" 2>&1 | awk -F': ' '/scheduling policy/ { print $2 }')

_TESTS_RUN=$((_TESTS_RUN + 1))
case "$_pol" in
    SCHED_FIFO*)
        echo "OK: policy=$_pol"
        ;;
    *)
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: expected SCHED_FIFO got '$_pol'"
        ;;
esac

test_summary
