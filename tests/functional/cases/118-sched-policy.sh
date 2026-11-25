#!/bin/sh
# Test: sched-policy = fifo sets SCHED_FIFO; read from /proc/PID/stat.
# (It used chrt, which the VM lacks, so it skipped on every run.)

# policy and rt_priority from /proc/PID/stat (fields 41 and 40),
# counted after the ")" that ends the command name. 0 = SCHED_OTHER,
# 1 = FIFO, 2 = RR, 6 = DEADLINE.
sched_of() {
    sed 's/.*) //' "/proc/$1/stat" 2>/dev/null | awk '{ print $39, $38 }'
}

SVC="test-schedp"

cat > "/etc/slinit.d/$SVC" <<EOF
type = process
sched-policy = fifo
sched-priority = 10
command = /bin/sh -c 'while :; do sleep 60; done'
restart = no
EOF

slinitctl --system start "$SVC" 2>/dev/null
wait_for_service "$SVC" STARTED 10
assert_service_state "$SVC" "STARTED" "service reached STARTED"

_pid=$(slinitctl --system status "$SVC" 2>/dev/null | awk '/PID:/ { print $2; exit }')

assert_eq "$(sched_of "$_pid")" "1 10" "policy FIFO (1), priority 10"

test_summary
