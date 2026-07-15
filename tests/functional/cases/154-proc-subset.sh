#!/bin/sh
# Test: proc-subset = pid remounts /proc with subset=pid so only PID
# directories are exposed. Kernel state files (/proc/uptime,
# /proc/meminfo, /proc/cpuinfo, /proc/net/*) return ENOENT. The
# child's own PID dir must still exist.

wait_for_service "ps-svc" "STARTED" 15

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ ! -f /var/tmp/ps-out/result ]; then
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: probe did not write its result file"
    test_summary
    return
fi
echo "OK: probe wrote result"

result=$(cat /var/tmp/ps-out/result 2>/dev/null)
self=$(echo "$result" | sed -n 's/.*self=\([^ ]*\).*/\1/p')
uptime=$(echo "$result" | sed -n 's/.*uptime=\([^ ]*\).*/\1/p')
meminfo=$(echo "$result" | sed -n 's/.*meminfo=\([^ ]*\).*/\1/p')

assert_eq "$self" "visible" "child's own PID dir still present"
assert_eq "$uptime" "hidden" "/proc/uptime hidden under subset=pid"
assert_eq "$meminfo" "hidden" "/proc/meminfo hidden under subset=pid"

# No host-side check on /proc/uptime: mount-propagation semantics
# between the test-runner service and ps-svc's ns depend on the
# initial shared/slave state of /proc, which the VM's initramfs
# does not guarantee. The primary invariant — subset=pid is applied
# in the service ns — is covered by the three assertions above.

test_summary
