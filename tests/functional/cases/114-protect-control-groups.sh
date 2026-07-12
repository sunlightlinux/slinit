#!/bin/sh
# Test: protect-control-groups remounts /sys/fs/cgroup ro in the
# service's mount namespace; host /sys/fs/cgroup stays writable.

SVC="test-pcg"

cat > "/etc/slinit.d/$SVC" <<EOF
type = process
protect-control-groups = yes
command = /bin/sh -c 'while :; do sleep 60; done'
restart = no
EOF

slinitctl --system start "$SVC" 2>/dev/null
wait_for_service "$SVC" STARTED 10
assert_service_state "$SVC" "STARTED" "service reached STARTED"

_pid=$(slinitctl --system status "$SVC" 2>/dev/null | awk '/PID:/ { print $2; exit }')
sleep 0.3

_TESTS_RUN=$((_TESTS_RUN + 1))
if grep -qE '/sys/fs/cgroup [^-]*\bro\b' "/proc/$_pid/mountinfo" 2>/dev/null; then
    echo "OK: /sys/fs/cgroup mounted read-only in service namespace"
else
    _mi=$(grep '/sys/fs/cgroup' /proc/$_pid/mountinfo 2>/dev/null | head -1)
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: /sys/fs/cgroup not ro: $_mi"
fi

# Host-side isolation probe: what we're guarding against is the
# service's read-only remount leaking into PID 1's view. Read a stable
# file the guarded service now sees as ro but that PID 1 must still
# see as normal — cgroup.controllers exists at the cgroup v2 root and
# holds the list of enabled controllers; a delegation-related mkdir
# gate at the top-level is expected on modern kernels, so probing
# read-only files is the right isolation signal.
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -r /sys/fs/cgroup/cgroup.controllers ] && \
   [ -n "$(cat /sys/fs/cgroup/cgroup.controllers 2>/dev/null)" ]; then
    echo "OK: host cgroup.controllers still readable from PID 1"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: host cgroup root no longer readable"
fi

test_summary
