#!/bin/sh
# Test: expanded cgroup-v2 directive set (memory-high/-low/-min,
# swap-max, cpu-max, io-weight, cpuset-cpus, cgroup-setting,
# startup-allowed-cpus/-memory-nodes). Baseline cgroup / memory-max /
# pids-max / cpu-weight is covered by 135-cgroup-v2.sh.
#
# Every knob is read back from the service's cgroup. A write that
# failed is only logged and the service still reaches STARTED, so the
# state alone proved nothing. (The case used to check memory.high and
# print a note instead of failing when it was wrong.)
# startup-allowed-* only act during boot bring-up; they stay parse
# coverage.

if [ ! -e /sys/fs/cgroup/cgroup.controllers ]; then
    skip_case "cgroup v2 not present"
fi

wait_for_service "cg-svc" "STARTED" 15
assert_service_state "cg-svc" "STARTED" "expanded cgroup-v2 set parses + service starts"

CG=/sys/fs/cgroup/slinit/184-cg
echo "INFO: controllers in $CG: $(cat "$CG/cgroup.controllers" 2>/dev/null)"

knob() {
    # knob FILE EXPECTED DIRECTIVE
    _TESTS_RUN=$((_TESTS_RUN + 1))
    _v=$(cat "$CG/$1" 2>/dev/null)
    if [ "$_v" = "$2" ]; then
        echo "OK: $3 landed ($1 = $_v)"
    else
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: $3: $1 = '$_v', expected '$2'"
    fi
}

knob memory.high      134217728        "cgroup-memory-high = 128M"
knob memory.low       33554432         "cgroup-memory-low = 32M"
knob memory.min       16777216         "cgroup-memory-min = 16M"
knob memory.swap.max  0                "cgroup-swap-max = 0"
knob cpu.max          "50000 100000"   "cgroup-cpu-max"
# io.weight exists only with blk-iocost or BFQ; the VM kernel has
# neither, and the controller list above already shows io was enabled.
if [ -e "$CG/io.weight" ]; then
    knob io.weight    "default 100"    "cgroup-io-weight = 100"
else
    echo "INFO: no io.weight on this kernel; cgroup-io-weight not checked"
fi
knob cpuset.cpus      0                "cgroup-cpuset-cpus = 0"
knob memory.oom.group 1                "cgroup-setting = memory.oom.group 1"

test_summary
