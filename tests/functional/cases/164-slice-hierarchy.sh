#!/bin/sh
# Test: slice = system.slice makes effective cgroup path
# /sys/fs/cgroup/system.slice/sliced-svc; verify via /proc/PID/cgroup.
if [ ! -e /sys/fs/cgroup/cgroup.controllers ]; then
    _TESTS_RUN=$((_TESTS_RUN + 1))
    echo "SKIP: cgroup v2 not the mounted hierarchy"
    test_summary
    exit 0
fi

wait_for_service "sliced-svc" "STARTED" 10
_pid=$(slinitctl --system status sliced-svc 2>/dev/null | awk '/PID:/ {print $2; exit}')
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -z "$_pid" ] || [ "$_pid" = "0" ]; then
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: no live PID"
    test_summary
    exit 0
fi

_cg=$(cat /proc/$_pid/cgroup 2>/dev/null)
_TESTS_RUN=$((_TESTS_RUN + 1))
case "$_cg" in
    *system.slice/sliced-svc*)
        echo "OK: PID $_pid in /system.slice/sliced-svc ($_cg)"
        ;;
    *)
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: cgroup is '$_cg', want /system.slice/sliced-svc"
        ;;
esac

test_summary
