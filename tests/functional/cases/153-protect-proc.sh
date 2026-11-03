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

# The host's /proc must be untouched. On ceres, after the perf suite ran
# a scripted service with protect-proc + proc-subset, a second procfs
# with hidepid=invisible,subset=pid sat on top of the host's /proc and
# /proc/meminfo, /proc/stat and /proc/uptime were gone (htop, free and
# uptime stopped working). Only the service's namespace may see that.
_e=0
while [ "$_e" -lt 15 ]; do
    case "$(svc_state pp2-svc)" in
        STARTED|STOPPED|FAILED) break ;;
    esac
    sleep 1; _e=$((_e + 1))
done
echo "MEASURE: pp2-svc state $(svc_state pp2-svc)"
_host_proc=$(awk '$5 == "/proc" { print $3, $NF }' /proc/self/mountinfo | tr '\n' ';')
echo "MEASURE: host /proc mounts: $_host_proc"
_TESTS_RUN=$((_TESTS_RUN + 1))
case "$_host_proc" in
    *hidepid*|*subset*)
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: a service's /proc options reached the host: $_host_proc"
        ;;
    *) echo "OK: host /proc carries no hidepid/subset" ;;
esac
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -r /proc/meminfo ] && [ -r /proc/stat ]; then
    echo "OK: host /proc/meminfo and /proc/stat readable"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: host /proc lost meminfo/stat"
fi

test_summary
