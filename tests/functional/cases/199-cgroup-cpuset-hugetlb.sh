#!/bin/sh
# Test: cgroup-cpuset-mems, cgroup-hugetlb, cpuset-partition parse
# and don't break the boot. Behaviour of each depends on kernel
# controller availability; a passing STARTED means the loader
# accepted each write attempt (fail-open on unsupported controllers).

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ ! -e /sys/fs/cgroup/cgroup.controllers ]; then
    echo "SKIP: cgroup v2 not present"
    test_summary
    return 0
fi
echo "OK: cgroup v2 hierarchy present"

wait_for_service "cgxtra-svc" "STARTED" 15
assert_service_state "cgxtra-svc" "STARTED" "cgroup-cpuset-mems + hugetlb + cpuset-partition parses + starts"

test_summary
