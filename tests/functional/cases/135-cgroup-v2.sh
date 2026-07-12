#!/bin/sh
# Test: cgroup v2 controller knobs (memory.max, memory.high, pids.max,
# cpu.weight) apply to a service's cgroup.

SVC="test-cgroup"
CG_ROOT="/sys/fs/cgroup/slinit/${SVC}"

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ ! -e /sys/fs/cgroup/cgroup.controllers ]; then
    echo "SKIP: /sys/fs/cgroup is not cgroup v2"
    test_summary
    return 0
fi
echo "OK: cgroup v2 hierarchy present"

# Ensure memory + pids + cpu are delegated. In functional's Alpine root
# they may not be — try to enable them ourselves (we're PID 1 in the
# guest, we CAN write to subtree_control).
_root_subtree=$(cat /sys/fs/cgroup/cgroup.subtree_control 2>/dev/null)
case "$_root_subtree" in
    *memory*pids*cpu*|*memory*cpu*pids*|*pids*memory*cpu*|*pids*cpu*memory*|*cpu*memory*pids*|*cpu*pids*memory*) ;;
    *)
        echo "+memory +pids +cpu" > /sys/fs/cgroup/cgroup.subtree_control 2>/dev/null || true
        _root_subtree=$(cat /sys/fs/cgroup/cgroup.subtree_control 2>/dev/null)
        ;;
esac
_TESTS_RUN=$((_TESTS_RUN + 1))
case "$_root_subtree" in
    *memory*|*pids*|*cpu*)
        echo "OK: controllers delegated at root ($_root_subtree)" ;;
    *)
        echo "SKIP: cgroup.subtree_control lacks required controllers"
        test_summary
        return 0 ;;
esac

cat > "/etc/slinit.d/$SVC" <<EOF
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
assert_service_state "$SVC" "STARTED" "service reached STARTED"

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -d "$CG_ROOT" ]; then
    echo "OK: cgroup directory created at $CG_ROOT"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: no cgroup directory at $CG_ROOT"
    test_summary
    return 0
fi

_check_knob() {
    _knob="$1"; _want="$2"; _label="$3"
    _TESTS_RUN=$((_TESTS_RUN + 1))
    _val=$(cat "$CG_ROOT/$_knob" 2>/dev/null)
    case "$_val" in
        *"$_want"*) echo "OK: $_label = $_val" ;;
        *) _TESTS_FAILED=$((_TESTS_FAILED + 1)); echo "FAIL: $_label expected match on '$_want' — got '$_val'" ;;
    esac
}
_check_knob memory.max    "134217728" "memory.max = 128M"
_check_knob memory.high   "67108864"  "memory.high = 64M"
_check_knob pids.max      "25"        "pids.max = 25"
_check_knob cpu.weight    "200"       "cpu.weight = 200"

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -s "$CG_ROOT/cgroup.procs" ]; then
    echo "OK: cgroup.procs has member(s): $(wc -l <"$CG_ROOT/cgroup.procs")"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: cgroup.procs is empty"
fi

test_summary
