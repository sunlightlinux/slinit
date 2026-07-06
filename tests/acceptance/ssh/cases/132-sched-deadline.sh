#!/bin/sh
# 132-sched-deadline — SCHED_DEADLINE with runtime/deadline/period.

SVC="${ACCEPTANCE_NS_PREFIX}schedd"

cleanup() {
    svc_remove "$SVC"
}
trap cleanup EXIT INT TERM
cleanup

svc_deploy "$SVC" <<EOF
type = process
sched-policy = deadline
sched-runtime = 10ms
sched-deadline = 100ms
sched-period = 100ms
command = /bin/sh -c 'while :; do sleep 60; done'
restart = no
EOF

slinitctl --system start "$SVC" 2>/dev/null
wait_for_service "$SVC" STARTED 10
assert_eq "$(svc_state "$SVC")" "STARTED" "service reached STARTED"

_pid=$(slinitctl --system status "$SVC" 2>/dev/null | awk '/^  PID:/ { print $2 }')
_pol=$(chrt -p "$_pid" 2>&1 | awk -F': ' '/scheduling policy/ { print $2 }')

_TESTS_RUN=$((_TESTS_RUN + 1))
case "$_pol" in
    SCHED_DEADLINE*) echo "OK: policy=$_pol" ;;
    *) _TESTS_FAILED=$((_TESTS_FAILED + 1)); echo "FAIL: policy '$_pol'" ;;
esac

test_summary
