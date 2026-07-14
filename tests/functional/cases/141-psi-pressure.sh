#!/bin/sh
# Test: memory-pressure-watch arms a PSI trigger on the service's
# cgroup memory.pressure file. The kernel keeps the trigger only
# while the writer fd is open, so we verify by looking at slinit's
# open fds (via /proc/1/fd) for a link into the service's cgroup
# pressure file.
#
# Full end-to-end verification would require actually stalling the
# cgroup on memory, which is fiddly in a quiet VM — arming the
# trigger is the invariant the daemon promises, and losing it would
# be the regression this test catches.

SVC="test-psi"
CG_ROOT="/sys/fs/cgroup/slinit/${SVC}"

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ ! -e /sys/fs/cgroup/cgroup.controllers ]; then
    echo "SKIP: cgroup v2 not present"
    test_summary
    return 0
fi
echo "OK: cgroup v2 hierarchy present"

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ ! -e /proc/pressure/memory ]; then
    echo "SKIP: kernel lacks PSI support (/proc/pressure/memory missing)"
    test_summary
    return 0
fi
echo "OK: kernel PSI support present"

# Delegate memory controller so <cgroup>/memory.pressure appears.
_root_subtree=$(cat /sys/fs/cgroup/cgroup.subtree_control 2>/dev/null)
case "$_root_subtree" in
    *memory*) ;;
    *)
        echo "+memory" > /sys/fs/cgroup/cgroup.subtree_control 2>/dev/null || true
        ;;
esac

cat > "/etc/slinit.d/$SVC" <<EOF
type = process
command = /bin/sh -c 'while true; do sleep 60; done'
cgroup = /sys/fs/cgroup/slinit/${SVC}
memory-pressure-watch = yes
memory-pressure-threshold = 150ms
EOF

slinitctl --system start "$SVC" 2>/dev/null
wait_for_service "$SVC" STARTED 10
assert_service_state "$SVC" "STARTED" "service reached STARTED"

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ ! -e "$CG_ROOT/memory.pressure" ]; then
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: $CG_ROOT/memory.pressure missing (memory controller not delegated?)"
    test_summary
    return 0
fi
echo "OK: memory.pressure present under service cgroup"

# The watcher holds an fd to memory.pressure open for the lifetime
# of STARTED. Scan slinit's fd list (PID 1) for a symlink into the
# service cgroup dir.
_found=""
for fd in /proc/1/fd/*; do
    _target=$(readlink "$fd" 2>/dev/null)
    case "$_target" in
        "$CG_ROOT/memory.pressure"|"$CG_ROOT/memory.pressure ("*")")
            _found="$fd → $_target"
            break
            ;;
    esac
done

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -n "$_found" ]; then
    echo "OK: PSI trigger fd held open by slinit ($_found)"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: no fd in /proc/1/fd/ points at $CG_ROOT/memory.pressure"
fi

# Stop the service — closing the fd should tear down the trigger.
# The subsequent scan should find no such fd.
slinitctl --system stop "$SVC" 2>/dev/null
wait_for_service "$SVC" STOPPED 10

_still_found=""
for fd in /proc/1/fd/*; do
    _target=$(readlink "$fd" 2>/dev/null)
    case "$_target" in
        "$CG_ROOT/memory.pressure"|"$CG_ROOT/memory.pressure ("*")")
            _still_found="$fd"
            break
            ;;
    esac
done

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -z "$_still_found" ]; then
    echo "OK: PSI trigger fd released after stop"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: PSI trigger fd $_still_found still open after stop"
fi

test_summary
