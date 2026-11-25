#!/bin/sh
# Test: sched-reset-on-fork = yes sets RESET_ON_FORK on the RT parent
# so forked+exec'd children revert to SCHED_OTHER.
# The service shell forks `sleep`; that child must be SCHED_OTHER while
# the shell stays FIFO 20. (It also accepted a FIFO parent alone when
# chrt did not show the flag.)
# (It used chrt, which the VM lacks, so it skipped on every run.)

# policy and rt_priority from /proc/PID/stat (fields 41 and 40),
# counted after the ")" that ends the command name. 0 = SCHED_OTHER,
# 1 = FIFO, 2 = RR, 6 = DEADLINE.
sched_of() {
    sed 's/.*) //' "/proc/$1/stat" 2>/dev/null | awk '{ print $39, $38 }'
}

SVC="test-schedrof"

cat > "/etc/slinit.d/$SVC" <<EOF
type = process
sched-policy = fifo
sched-priority = 20
sched-reset-on-fork = yes
command = /bin/sh -c 'sleep 1; while :; do sleep 60; done'
restart = no
EOF

slinitctl --system start "$SVC" 2>/dev/null
wait_for_service "$SVC" STARTED 10
assert_service_state "$SVC" "STARTED" "service reached STARTED"

_pid=$(slinitctl --system status "$SVC" 2>/dev/null | awk '/PID:/ { print $2; exit }')

assert_eq "$(sched_of "$_pid")" "1 20" "service shell is FIFO 20"
# Wait for the `sleep 60`, which the shell forks only after `sleep 1`,
# so after slinit has set the policy.
_child=""
_e=0
while [ -z "$_child" ] && [ "$_e" -lt 20 ]; do
    _child=$(pgrep -P "$_pid" -f "sleep 60" | head -1)
    [ -n "$_child" ] || { sleep 0.3; _e=$((_e + 1)); }
done
assert_eq "$(sched_of "$_child")" "0 0" "forked sleep ($_child) reverted to SCHED_OTHER"

test_summary
