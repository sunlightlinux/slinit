#!/bin/sh
# Test: sched-policy = rr + sched-priority = 42 applies SCHED_RR and
# priority 42, verified via chrt -p.

if ! command -v chrt >/dev/null 2>&1; then
    echo "SKIP: chrt not on target (util-linux not installed)"
    test_summary
    return 0
fi

SVC="test-schedpr"

cat > "/etc/slinit.d/$SVC" <<EOF
type = process
sched-policy = rr
sched-priority = 42
command = /bin/sh -c 'while :; do sleep 60; done'
restart = no
EOF

slinitctl --system start "$SVC" 2>/dev/null
wait_for_service "$SVC" STARTED 10
assert_service_state "$SVC" "STARTED" "service reached STARTED"

_pid=$(slinitctl --system status "$SVC" 2>/dev/null | awk '/PID:/ { print $2; exit }')
_prio=$(chrt -p "$_pid" 2>&1 | awk -F': ' '/scheduling priority/ { print $2 }')
_pol=$(chrt -p "$_pid" 2>&1 | awk -F': ' '/scheduling policy/ { print $2 }')

_TESTS_RUN=$((_TESTS_RUN + 1))
case "$_pol" in
    SCHED_RR*) echo "OK: policy=$_pol" ;;
    *) _TESTS_FAILED=$((_TESTS_FAILED + 1)); echo "FAIL: policy '$_pol'" ;;
esac
assert_eq "$_prio" "42" "priority = 42"

test_summary
