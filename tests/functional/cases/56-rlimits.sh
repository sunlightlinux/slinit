#!/bin/sh
# Test: resource limits (rlimits) are applied to service processes.
# Validates: rlimit-nofile, rlimit-core settings via /proc/PID/limits.

wait_for_service "rlimit-svc" "STARTED" 10

pid=$(slinitctl --system status rlimit-svc 2>/dev/null | grep 'PID:' | awk '{print $2}')

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -z "$pid" ] || [ ! -f "/proc/$pid/limits" ]; then
    echo "FAIL: could not read /proc/$pid/limits"
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
else
    echo "OK: process $pid found"
fi

# Check rlimit-nofile: soft=1024, hard=4096
nofile_line=$(grep "Max open files" /proc/$pid/limits 2>/dev/null)
nofile_soft=$(echo "$nofile_line" | awk '{print $4}')
nofile_hard=$(echo "$nofile_line" | awk '{print $5}')

assert_eq "$nofile_soft" "1024" "rlimit-nofile soft = 1024"
assert_eq "$nofile_hard" "4096" "rlimit-nofile hard = 4096"

# Check rlimit-core: soft=0 (core dumps disabled)
core_line=$(grep "Max core file size" /proc/$pid/limits 2>/dev/null)
core_soft=$(echo "$core_line" | awk '{print $5}')
assert_eq "$core_soft" "0" "rlimit-core soft = 0"

assert_service_state "rlimit-svc" "STARTED" "rlimit-svc is STARTED"

test_summary
