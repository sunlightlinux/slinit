#!/bin/sh
# Test: slinitctl freeze/thaw writes 1/0 to cgroup.freeze. Skip if
# cgroup v2 hierarchy isn't the mounted one (unlikely on modern
# kernel but worth guarding).
if [ ! -e /sys/fs/cgroup/cgroup.controllers ]; then
    _TESTS_RUN=$((_TESTS_RUN + 1))
    echo "SKIP: cgroup v2 not the mounted hierarchy"
    test_summary
    exit 0
fi

wait_for_service "freeze-svc" "STARTED" 10
CG=/sys/fs/cgroup/slinit/freeze-svc
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ ! -e "$CG/cgroup.freeze" ]; then
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: no cgroup.freeze at $CG"
    test_summary
    exit 0
else
    echo "OK: freeze knob exists"
fi

slinitctl --system freeze freeze-svc >/dev/null 2>&1
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$(cat $CG/cgroup.freeze 2>/dev/null)" = "1" ]; then
    echo "OK: cgroup.freeze = 1 after freeze"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: cgroup.freeze != 1 after freeze"
fi

slinitctl --system thaw freeze-svc >/dev/null 2>&1
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$(cat $CG/cgroup.freeze 2>/dev/null)" = "0" ]; then
    echo "OK: cgroup.freeze = 0 after thaw"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: cgroup.freeze != 0 after thaw"
fi

test_summary
