#!/bin/sh
# Test: process attributes reach a service's first child.
#
# slinit sets nice, rlimits, affinity, scheduling, ioprio and
# oom_score_adj on the child from outside, after it has started. The
# shell below forks `sleep` at once; it used to run as SCHED_OTHER even
# with sched-reset-on-fork = no, because the policy arrived after the
# fork. slinit-runner now waits at a start gate until they are set.

SVC=test-first-child
printf '%s\n' 'type = process' 'sched-policy = fifo' 'sched-priority = 20' \
    'sched-reset-on-fork = no' 'rlimit-nofile = 123' \
    'command = /bin/sh -c "sleep 60; true"' 'restart = false' \
    > /etc/slinit.d/$SVC

slinitctl --system start $SVC >/dev/null 2>&1
wait_for_service $SVC STARTED 10 || true
assert_service_state $SVC STARTED "$SVC started"

_pid=$(slinitctl --system status $SVC 2>/dev/null | awk '/PID:/ { print $2; exit }')
_child=""
_e=0
while [ -z "$_child" ] && [ "$_e" -lt 10 ]; do
    _child=$(pgrep -P "$_pid" sleep | head -1)
    [ -n "$_child" ] || { sleep 0.2; _e=$((_e + 1)); }
done

# policy and rt_priority: fields 41 and 40, counted after the ")".
sched_of() {
    sed 's/.*) //' "/proc/$1/stat" 2>/dev/null | awk '{ print $39, $38 }'
}
assert_eq "$(sched_of "$_child")" "1 20" "the first child ($_child) inherited FIFO 20"
assert_eq "$(awk '/^Max open files/ { print $4 }' /proc/$_child/limits 2>/dev/null)" "123" \
    "the first child inherited rlimit-nofile"

slinitctl --system stop $SVC >/dev/null 2>&1
test_summary
