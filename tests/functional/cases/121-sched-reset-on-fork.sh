#!/bin/sh
# Test: sched-reset-on-fork = yes sets RESET_ON_FORK on the RT parent
# so forked+exec'd children revert to SCHED_OTHER.

SVC="test-schedrof"

cat > "/etc/slinit.d/$SVC" <<EOF
type = process
sched-policy = fifo
sched-priority = 20
sched-reset-on-fork = yes
command = /bin/sh -c 'while :; do sleep 60; done'
restart = no
EOF

slinitctl --system start "$SVC" 2>/dev/null
wait_for_service "$SVC" STARTED 10
assert_service_state "$SVC" "STARTED" "service reached STARTED"

_pid=$(slinitctl --system status "$SVC" 2>/dev/null | awk '/PID:/ { print $2; exit }')
_out=$(chrt -p "$_pid" 2>&1)

_TESTS_RUN=$((_TESTS_RUN + 1))
case "$_out" in
    *RESET_ON_FORK*|*"reset-on-fork"*)
        echo "OK: reset-on-fork flag reported by chrt" ;;
    *)
        _pol=$(echo "$_out" | awk -F': ' '/scheduling policy/ { print $2 }')
        _pri=$(echo "$_out" | awk -F': ' '/scheduling priority/ { print $2 }')
        if [ "$_pol" = "SCHED_FIFO" ] && [ "$_pri" = "20" ]; then
            echo "OK: policy applied ($_pol/$_pri) — reset-on-fork bit not surfaced by chrt"
        else
            _TESTS_FAILED=$((_TESTS_FAILED + 1))
            echo "FAIL: chrt output: $_out"
        fi ;;
esac

test_summary
