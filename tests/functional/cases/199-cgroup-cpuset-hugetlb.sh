#!/bin/sh
# Test: cgroup-cpuset-mems, cgroup-hugetlb and cpuset-partition reach
# the service's cgroup.
#
# Each knob is read back. A failed write is only logged and the service
# still reaches STARTED, so the state alone proved nothing. (The case
# used to check STARTED only, with a cgroup path outside /sys/fs/cgroup
# that slinit ignores, and on a VM with no cgroup2 mounted, so it
# skipped on every run.) member is the kernel default, so the
# partition is set to root, which reads back as "root" or, when the
# kernel cannot grant it, "root invalid (...)": either way the write
# arrived. The hugetlb limit is in bytes: 8M is four 2 MB pages.

if [ ! -e /sys/fs/cgroup/cgroup.controllers ]; then
    skip_case "cgroup v2 not present"
fi

wait_for_service "cgxtra-svc" "STARTED" 15
assert_service_state "cgxtra-svc" "STARTED" "cgroup-cpuset-mems + hugetlb + cpuset-partition parses + starts"

CG=/sys/fs/cgroup/slinit/199-cg
echo "INFO: controllers in $CG: $(cat "$CG/cgroup.controllers" 2>/dev/null)"

_TESTS_RUN=$((_TESTS_RUN + 1))
_v=$(cat "$CG/cpuset.mems" 2>/dev/null)
if [ "$_v" = "0" ]; then
    echo "OK: cgroup-cpuset-mems landed (cpuset.mems = $_v)"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: cpuset.mems = '$_v', expected '0'"
fi

_TESTS_RUN=$((_TESTS_RUN + 1))
_v=$(cat "$CG/cpuset.cpus.partition" 2>/dev/null)
case "$_v" in
    root*)
        echo "OK: cpuset-partition landed (cpuset.cpus.partition = $_v)" ;;
    *)
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: cpuset.cpus.partition = '$_v', expected root" ;;
esac

# hugetlb is a controller only on kernels built with it.
if [ -e "$CG/hugetlb.2MB.max" ]; then
    _TESTS_RUN=$((_TESTS_RUN + 1))
    _v=$(cat "$CG/hugetlb.2MB.max" 2>/dev/null)
    if [ "$_v" = "8388608" ]; then
        echo "OK: cgroup-hugetlb landed (hugetlb.2MB.max = $_v)"
    else
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: hugetlb.2MB.max = '$_v', expected '8388608' (8M)"
    fi
else
    echo "INFO: no hugetlb controller on this kernel; cgroup-hugetlb not checked"
fi

test_summary
