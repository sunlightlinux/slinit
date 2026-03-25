#!/bin/sh
# Test: cpu-affinity pins process to specified CPUs.
# Validates: parseCPUAffinity, applyCPUAffinity (sched_setaffinity).

wait_for_service "cpu-test" "STARTED" 10

# Get the PID from service status
pid=$(slinitctl --system status cpu-test 2>/dev/null | grep 'PID:' | awk '{print $2}')

# Read the affinity mask from /proc/PID/status
affinity=$(grep Cpus_allowed_list /proc/$pid/status 2>/dev/null | awk '{print $2}')
assert_eq "$affinity" "0" "cpu-affinity is 0"

# Verify service is running
assert_service_state "cpu-test" "STARTED" "cpu-test is STARTED"

test_summary
