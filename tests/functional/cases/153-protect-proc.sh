#!/bin/sh
# Test: protect-proc = invisible remounts /proc with hidepid=invisible
# so PIDs owned by other UIDs disappear from the mount namespace. The
# service runs as nobody; PID 1 (root) must be invisible; the child's
# own PID must remain visible so /proc/self doesn't break.

wait_for_service "pp-svc" "STARTED" 15

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ ! -f /var/tmp/pp-out/result ]; then
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: probe did not write its result file"
    test_summary
    return
fi
echo "OK: probe wrote result"

result=$(cat /var/tmp/pp-out/result 2>/dev/null)
self=$(echo "$result" | sed -n 's/.*self=\([^ ]*\).*/\1/p')
one=$(echo "$result" | sed -n 's/.*one=\([^ ]*\).*/\1/p')

assert_eq "$self" "visible" "child sees its own /proc/PID/ dir"
assert_eq "$one" "invisible" "child cannot see /proc/1 (root-owned)"

test_summary
