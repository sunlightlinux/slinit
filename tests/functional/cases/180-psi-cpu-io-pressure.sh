#!/bin/sh
# Test: cpu-pressure-watch/threshold + io-pressure-watch/threshold arm
# a PSI trigger (memory PSI is covered by 141).
#
# An armed watch keeps the cgroup's cpu.pressure / io.pressure open in
# PID 1 with the trigger written; a file that failed to open, or a
# trigger the kernel refused, is logged and closed, and the service
# still reaches STARTED. So the open fds are what is checked. (The
# case used to check STARTED only, with cgroup paths outside
# /sys/fs/cgroup that slinit ignores, on a VM with no cgroup2 mounted.)

if [ ! -e /sys/fs/cgroup/cgroup.controllers ]; then
    skip_case "cgroup v2 not present"
fi
if [ ! -e /proc/pressure/cpu ]; then
    skip_case "kernel lacks PSI support"
fi

wait_for_service "cpupsi-svc" "STARTED" 10
wait_for_service "iopsi-svc" "STARTED" 10
assert_service_state "cpupsi-svc" "STARTED" "cpu-pressure-watch/threshold parses + service starts"
assert_service_state "iopsi-svc" "STARTED" "io-pressure-watch/threshold parses + service starts"

held() {
    # held FILE LABEL: PID 1 has FILE open.
    _TESTS_RUN=$((_TESTS_RUN + 1))
    if ls -l /proc/1/fd 2>/dev/null | grep -q " -> $1\$"; then
        echo "OK: $2 armed (PID 1 holds $1)"
    else
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: $2: PID 1 does not hold $1 open"
    fi
}

held /sys/fs/cgroup/slinit/180-cpu/cpu.pressure "cpu-pressure-watch"
held /sys/fs/cgroup/slinit/180-io/io.pressure "io-pressure-watch"

test_summary
