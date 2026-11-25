#!/bin/sh
# Test: cpu-affinity pins process to specified CPUs.
# Validates: parseCPUAffinity, applyCPUAffinity (sched_setaffinity).
#
# The VM gets two CPUs (qemu-args) and the service is pinned to CPU 1, the
# one a process would never be confined to by default. On a single CPU the
# mask was "0" whether or not slinit applied anything.

wait_for_service "cpu-test" "STARTED" 10
wait_for_service "cpu-free" "STARTED" 10

ncpu=$(grep -c '^processor' /proc/cpuinfo)
if [ "$ncpu" -lt 2 ]; then
    skip_case "VM has $ncpu CPU(s); qemu-args -smp 2 not applied"
fi

affinity_of() {
    _pid=$(slinitctl --system status "$1" 2>/dev/null | awk '/PID:/ {print $2}')
    awk '/^Cpus_allowed_list:/ {print $2}' "/proc/$_pid/status" 2>/dev/null
}

# Control: an unpinned sibling may run on both CPUs, so the pin below is
# slinit's doing and not something the VM imposes on every service.
assert_eq "$(affinity_of cpu-free)" "0-1" "unpinned service may run on CPUs 0-1"
assert_eq "$(affinity_of cpu-test)" "1" "cpu-affinity = 1 confines the service to CPU 1"

assert_service_state "cpu-test" "STARTED" "cpu-test is STARTED"

test_summary
