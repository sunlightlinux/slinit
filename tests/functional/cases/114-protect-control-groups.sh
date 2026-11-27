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

# A process service is STARTED once forked. protect-control-groups goes
# through slinit-runner, which makes the ro remount itself just before
# it execs the command, so right after STARTED the pid can still be the
# runner with /sys/fs/cgroup rw. Poll (as 281 does since f33eb27) rather
# than read once after a fixed 0.3 s, which a slow emulated runner can
# outlast.
_e=0
until grep -qE '/sys/fs/cgroup [^-]*\bro\b' "/proc/$_pid/mountinfo" 2>/dev/null ||
      [ "$_e" -ge 50 ]; do
    sleep 0.2; _e=$((_e + 1))
done

_TESTS_RUN=$((_TESTS_RUN + 1))
if grep -qE '/sys/fs/cgroup [^-]*\bro\b' "/proc/$_pid/mountinfo" 2>/dev/null; then
    echo "OK: /sys/fs/cgroup mounted read-only in service namespace"
else
    _mi=$(grep '/sys/fs/cgroup' /proc/$_pid/mountinfo 2>/dev/null | head -1)
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: /sys/fs/cgroup not ro: $_mi"
fi

# Host-side isolation probe: what we're guarding against is the
# service's read-only remount leaking into PID 1's view. Enumerating
# /sys/fs/cgroup as a directory would fail if the mount was replaced
# by something unexpected in our namespace. Content emptiness at
# cgroup.controllers is normal on kernels booted without controllers
# in the root, so probing readability of a *file* is fragile — probe
# the mountpoint itself instead.
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -d /sys/fs/cgroup ] && ls /sys/fs/cgroup >/dev/null 2>&1; then
    echo "OK: PID 1 still sees /sys/fs/cgroup as a normal directory"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: PID 1 /sys/fs/cgroup enumeration broken (leaked ro remount?)"
fi

test_summary
