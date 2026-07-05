#!/bin/sh
# 100-cgroup-v2 — apply cgroup v2 controller knobs (memory.max,
# memory.high, pids.max, cpu.weight) to a service and verify they
# land on the child's cgroup.

SVC="${ACCEPTANCE_NS_PREFIX}cgroup"
CG_ROOT="/sys/fs/cgroup/slinit/${SVC}"

cleanup() {
    svc_remove "$SVC"
    # Cgroup dir is auto-removed on service exit under normal
    # conditions; sanity-clean anyway in case the process left it
    # behind.
    [ -d "$CG_ROOT" ] && rmdir "$CG_ROOT" 2>/dev/null || true
}
trap cleanup EXIT INT TERM
cleanup

# Skip if cgroup v2 isn't the mounted hierarchy.
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ ! -e /sys/fs/cgroup/cgroup.controllers ]; then
    echo "SKIP: /sys/fs/cgroup is not cgroup v2 on this target"
    test_summary
    exit 0
fi
echo "OK: cgroup v2 hierarchy present"

# Skip if the operator hasn't enabled the controllers we need in
# cgroup.subtree_control. Without at least memory + pids + cpu
# delegated, slinit can create the cgroup dir but writing the knobs
# fails with ENOENT. Enabling them here would be too invasive
# (permanent global change), so this is a "target-not-provisioned"
# skip, not a slinit bug.
_TESTS_RUN=$((_TESTS_RUN + 1))
_root_subtree=$(cat /sys/fs/cgroup/cgroup.subtree_control 2>/dev/null)
case "$_root_subtree" in
    *memory*pids*cpu*|*memory*cpu*pids*|*pids*memory*cpu*|*pids*cpu*memory*|*cpu*memory*pids*|*cpu*pids*memory*)
        echo "OK: memory + pids + cpu controllers delegated at root ($_root_subtree)"
        ;;
    *)
        echo "SKIP: root cgroup.subtree_control lacks memory+pids+cpu ('$_root_subtree')"
        echo "      Fix: 'echo \"+memory +pids +cpu\" > /sys/fs/cgroup/cgroup.subtree_control'"
        test_summary
        exit 0
        ;;
esac

svc_deploy "$SVC" <<EOF
type = process
command = /bin/sh -c 'while true; do sleep 60; done'
cgroup = /sys/fs/cgroup/slinit/${SVC}
cgroup-memory-max = 128M
cgroup-memory-high = 64M
cgroup-pids-max = 25
cgroup-cpu-weight = 200
EOF

slinitctl --system start "$SVC" 2>/dev/null
wait_for_service "$SVC" STARTED 10
assert_eq "$(svc_state "$SVC")" "STARTED" "service reached STARTED"

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -d "$CG_ROOT" ]; then
    echo "OK: cgroup directory created at $CG_ROOT"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: no cgroup directory at $CG_ROOT"
    test_summary
    exit 0
fi

# Verify each knob landed with the requested value. Some kernels
# normalise sizes (e.g. 128M → 134217728) so accept either textual
# form.
_check_knob() {
    _knob="$1"; _want="$2"; _label="$3"
    _TESTS_RUN=$((_TESTS_RUN + 1))
    _val=$(cat "$CG_ROOT/$_knob" 2>/dev/null)
    case "$_val" in
        *"$_want"*)
            echo "OK: $_label = $_val"
            ;;
        *)
            _TESTS_FAILED=$((_TESTS_FAILED + 1))
            echo "FAIL: $_label expected match on '$_want' — got '$_val'"
            ;;
    esac
}
_check_knob memory.max    "134217728" "memory.max = 128M"
_check_knob memory.high   "67108864"  "memory.high = 64M"
_check_knob pids.max      "25"        "pids.max = 25"
_check_knob cpu.weight    "200"       "cpu.weight = 200"

# The service's process is a member of the cgroup.
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -s "$CG_ROOT/cgroup.procs" ]; then
    echo "OK: cgroup.procs has member(s): $(wc -l <"$CG_ROOT/cgroup.procs")"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: cgroup.procs is empty"
fi

test_summary
