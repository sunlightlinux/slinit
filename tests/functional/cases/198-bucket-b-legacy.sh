#!/bin/sh
# Test: Bucket B legacy directives (coredump-filter, ignore-sigpipe,
# memory-ksm, personality, remove-ipc, timer-slack-nsec) all parse,
# coexist, and the three /proc can show are applied to the process.
#
# The case used to check only that the service started, with
# coredump-filter and timer-slack-nsec set to the kernel's own defaults,
# so dropping any directive changed nothing it could see.

wait_for_service "bucketb-svc" "STARTED" 10
assert_service_state "bucketb-svc" "STARTED" "Bucket B legacy cluster parses + service starts"

pid=$(slinitctl --system status bucketb-svc 2>/dev/null | awk '/PID:/ {print $2; exit}')
# STARTED comes at fork; these attributes are set by slinit-runner just
# before it execs the command, so wait until the pid is no longer the
# runner (as 125 does) before reading them.
_wait_exec() { # pid
    _d=$(( $(date +%s) + 10 ))
    while [ "$(date +%s)" -lt "$_d" ] &&
          [ "$(cat "/proc/$1/comm" 2>/dev/null)" = slinit-runner ]; do
        sleep 0.2
    done
}
_wait_exec "$pid"
assert_eq "$(cat /proc/$pid/coredump_filter 2>/dev/null)" "0000003f" "coredump-filter = 0x3f applied"
assert_eq "$(cat /proc/$pid/timerslack_ns 2>/dev/null)" "120000" "timer-slack-nsec = 120000 applied"
assert_eq "$(cat /proc/$pid/personality 2>/dev/null)" "00000008" "personality = x86 is PER_LINUX32"

test_summary
