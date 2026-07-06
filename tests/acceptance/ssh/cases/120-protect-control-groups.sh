#!/bin/sh
# 120-protect-control-groups — remounts /sys/fs/cgroup read-only in
# the service's mount namespace.

SVC="${ACCEPTANCE_NS_PREFIX}pcg"

cleanup() {
    svc_remove "$SVC"
}
trap cleanup EXIT INT TERM
cleanup

svc_deploy "$SVC" <<EOF
type = process
protect-control-groups = yes
command = /bin/sh -c 'while :; do sleep 60; done'
restart = no
EOF

slinitctl --system start "$SVC" 2>/dev/null
wait_for_service "$SVC" STARTED 10
assert_eq "$(svc_state "$SVC")" "STARTED" "service reached STARTED"

_pid=$(slinitctl --system status "$SVC" 2>/dev/null | awk '/^  PID:/ { print $2 }')
sleep 0.3

# The mountinfo entry for /sys/fs/cgroup in the child's namespace
# must carry the `ro` flag.
_TESTS_RUN=$((_TESTS_RUN + 1))
if grep -qE '/sys/fs/cgroup [^-]*\bro\b' "/proc/$_pid/mountinfo" 2>/dev/null; then
    echo "OK: /sys/fs/cgroup mounted read-only in service namespace"
else
    _mi=$(grep '/sys/fs/cgroup' /proc/$_pid/mountinfo 2>/dev/null | head -1)
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: /sys/fs/cgroup not ro: $_mi"
fi

# Host's /sys/fs/cgroup remains writable (no leak). We probe by
# attempting to make a dummy subdir and rmdir immediately.
_TESTS_RUN=$((_TESTS_RUN + 1))
_probe="/sys/fs/cgroup/acceptance-pcg-probe.$$"
if mkdir "$_probe" 2>/dev/null; then
    rmdir "$_probe"
    echo "OK: host /sys/fs/cgroup still writable"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: host cgroup became read-only"
fi

test_summary
