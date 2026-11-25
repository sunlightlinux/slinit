#!/bin/sh
# Test: SCHED_DEADLINE via sched-runtime/deadline/period. Read the
# policy from /proc/PID/stat.
# (It used chrt, which the VM lacks, so it skipped on every run.)

# policy and rt_priority from /proc/PID/stat (fields 41 and 40),
# counted after the ")" that ends the command name. 0 = SCHED_OTHER,
# 1 = FIFO, 2 = RR, 6 = DEADLINE.
sched_of() {
    sed 's/.*) //' "/proc/$1/stat" 2>/dev/null | awk '{ print $39, $38 }'
}

SVC="test-schedd"

cat > "/etc/slinit.d/$SVC" <<EOF
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
assert_service_state "$SVC" "STARTED" "service reached STARTED"

_pid=$(slinitctl --system status "$SVC" 2>/dev/null | awk '/PID:/ { print $2; exit }')

assert_eq "$(sched_of "$_pid")" "6 0" "policy DEADLINE (6)"

test_summary
