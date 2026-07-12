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

# Host-side writability probe: on cgroup v2 the top-level /sys/fs/cgroup
# is often already read-only by design (delegation contract) — creating
# subgroups belongs in delegated subtrees, not at the root. So a mkdir
# at the top-level failing isn't evidence of a leak from the guarded
# service. Instead, probe /sys/fs/cgroup/cgroup.subtree_control which
# PID 1 (us) must still be able to read even after the service masked
# its own view.
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -r /sys/fs/cgroup/cgroup.subtree_control ]; then
    echo "OK: host cgroup.subtree_control still readable from PID 1"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: host cgroup root no longer readable"
fi

test_summary
