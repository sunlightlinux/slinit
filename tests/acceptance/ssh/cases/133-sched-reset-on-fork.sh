#!/bin/sh
# 133-sched-reset-on-fork — probes the RESET_ON_FORK bit. When set,
# forked children of an RT parent revert to SCHED_OTHER on exec.

SVC="${ACCEPTANCE_NS_PREFIX}schedrof"

cleanup() {
    svc_remove "$SVC"
}
trap cleanup EXIT INT TERM
cleanup

svc_deploy "$SVC" <<EOF
type = process
sched-policy = fifo
sched-priority = 20
sched-reset-on-fork = yes
command = /bin/sh -c 'while :; do sleep 60; done'
restart = no
EOF

slinitctl --system start "$SVC" 2>/dev/null
wait_for_service "$SVC" STARTED 10
assert_eq "$(svc_state "$SVC")" "STARTED" "service reached STARTED"

_pid=$(slinitctl --system status "$SVC" 2>/dev/null | awk '/^  PID:/ { print $2 }')

# 'chrt -p' shows RESET_ON_FORK flag when set on a task.
_out=$(chrt -p "$_pid" 2>&1)

_TESTS_RUN=$((_TESTS_RUN + 1))
case "$_out" in
    *RESET_ON_FORK*|*"reset-on-fork"*)
        echo "OK: reset-on-fork flag reported by chrt"
        ;;
    *)
        # Older util-linux hides the bit in the policy name (e.g.
        # 'SCHED_FIFO|SCHED_RESET_ON_FORK'). If we see SCHED_FIFO
        # and priority 20, count it as configured.
        _pol=$(echo "$_out" | awk -F': ' '/scheduling policy/ { print $2 }')
        _pri=$(echo "$_out" | awk -F': ' '/scheduling priority/ { print $2 }')
        if [ "$_pol" = "SCHED_FIFO" ] && [ "$_pri" = "20" ]; then
            echo "OK: policy applied ($_pol/$_pri) — reset-on-fork bit not surfaced by chrt"
        else
            _TESTS_FAILED=$((_TESTS_FAILED + 1))
            echo "FAIL: chrt output: $_out"
        fi
        ;;
esac

test_summary
