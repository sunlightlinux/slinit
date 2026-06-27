#!/bin/sh
# 59-options-kill-all-on-stop — verify `kill-all-on-stop = yes` reaches
# children that escaped the service's process group. Without the flag,
# only the pgroup signal lands (process.go:1086 SignalProcess uses -pid);
# a setsid'd grandchild survives. With the flag, killCgroupTree
# (process.go:1096 → KillCgroup) walks cgroup.procs and signals every
# PID regardless of session/pgroup.
#
# Sub-case A: flag set + setsid'd child → child dies after stop.
# Sub-case B: default control — same shell topology, no flag, same
#             dedicated cgroup → setsid'd child survives the stop.
#             Without this inverse, A passing wouldn't isolate the flag:
#             a wrong probe (e.g. child accidentally in pgroup) would
#             look identical regardless of kill-all-on-stop.
#
# Both services pin themselves to a dedicated cgroup so killCgroupTree
# has something to act on — DefaultCgroupPath is empty unless slinit was
# started with --cgroup-path, otherwise the call silently no-ops.

SVC_FLAG="acceptance-test-killall-on"
SVC_NOFLAG="acceptance-test-killall-off"
PIDFILE_ON="/run/acceptance-killall-on.pid"
PIDFILE_OFF="/run/acceptance-killall-off.pid"
CG_ON="/sys/fs/cgroup/slinit-acc-killall-on"
CG_OFF="/sys/fs/cgroup/slinit-acc-killall-off"

cleanup() {
    svc_remove "$SVC_FLAG" "$SVC_NOFLAG"
    # Reap any straggler child (sub-case B's setsid'd sleep, by design).
    for _f in "$PIDFILE_ON" "$PIDFILE_OFF"; do
        _p=$(cat "$_f" 2>/dev/null)
        case "$_p" in
            ""|*[!0-9]*) ;;
            *) kill -KILL "$_p" 2>/dev/null || true ;;
        esac
        rm -f "$_f"
    done
    # slinit auto-mkdir's the cgroup but never rmdir's it; do it ourselves
    # so re-runs start clean.
    rmdir "$CG_ON"  2>/dev/null || true
    rmdir "$CG_OFF" 2>/dev/null || true
}
trap cleanup EXIT INT TERM

if [ ! -e /sys/fs/cgroup/cgroup.controllers ]; then
    _TESTS_RUN=$((_TESTS_RUN + 1))
    echo "SKIP: cgroup v2 not mounted at /sys/fs/cgroup — flag is a no-op"
    test_summary
    exit 0
fi

if ! command -v setsid >/dev/null 2>&1; then
    _TESTS_RUN=$((_TESTS_RUN + 1))
    echo "SKIP: setsid not on target — can't escape pgroup for the probe"
    test_summary
    exit 0
fi

# Helper: poll up to 3s for the child to write its PID. The forked sh
# may not have flushed the redirect yet right after STARTED.
_read_child_pid() {
    _f="$1"; _e=0; _pid=""
    while [ "$_e" -lt 10 ]; do
        _pid=$(cat "$_f" 2>/dev/null)
        case "$_pid" in
            ""|*[!0-9]*) ;;
            *) echo "$_pid"; return 0 ;;
        esac
        sleep 0.3
        _e=$((_e + 1))
    done
    return 1
}

_wait_stopped() {
    _name="$1"; _e=0
    while [ "$_e" -lt 8 ]; do
        case "$(svc_state "$_name")" in STOPPED|"") return 0 ;; esac
        sleep 1
        _e=$((_e + 1))
    done
    return 1
}

# --- Sub-case A: kill-all-on-stop = yes ----------------------------------
rm -f "$PIDFILE_ON"
svc_deploy "$SVC_FLAG" <<EOF
type = process
cgroup = $CG_ON
options = kill-all-on-stop
command = /bin/sh -c 'setsid sh -c "exec sleep 3600" & echo \$! > $PIDFILE_ON; while :; do sleep 60; done'
restart = false
EOF

slinitctl --system start "$SVC_FLAG" >/dev/null 2>&1
wait_for_service "$SVC_FLAG" "STARTED" 10 || true
assert_service_state "$SVC_FLAG" "STARTED" "$SVC_FLAG STARTED"

_child_on=$(_read_child_pid "$PIDFILE_ON")
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -z "$_child_on" ] || ! kill -0 "$_child_on" 2>/dev/null; then
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: kill-all child pid unreadable or already dead ('$_child_on')"
else
    echo "OK: kill-all setsid'd child pid=$_child_on alive pre-stop"
fi

slinitctl --system stop "$SVC_FLAG" >/dev/null 2>&1
_wait_stopped "$SVC_FLAG"
# killCgroupTree with SIGTERM walks cgroup.procs synchronously, but the
# target processes still need to exit — give them a moment.
sleep 1

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -n "$_child_on" ] && kill -0 "$_child_on" 2>/dev/null; then
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: kill-all child $_child_on STILL alive after stop"
    kill -KILL "$_child_on" 2>/dev/null || true
else
    echo "OK: kill-all child reaped via cgroup walk"
fi

svc_remove "$SVC_FLAG"
rmdir "$CG_ON" 2>/dev/null || true

# --- Sub-case B: control (no kill-all-on-stop) ---------------------------
rm -f "$PIDFILE_OFF"
svc_deploy "$SVC_NOFLAG" <<EOF
type = process
cgroup = $CG_OFF
command = /bin/sh -c 'setsid sh -c "exec sleep 3600" & echo \$! > $PIDFILE_OFF; while :; do sleep 60; done'
restart = false
EOF

slinitctl --system start "$SVC_NOFLAG" >/dev/null 2>&1
wait_for_service "$SVC_NOFLAG" "STARTED" 10 || true
assert_service_state "$SVC_NOFLAG" "STARTED" "$SVC_NOFLAG STARTED"

_child_off=$(_read_child_pid "$PIDFILE_OFF")
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -z "$_child_off" ] || ! kill -0 "$_child_off" 2>/dev/null; then
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: control child pid unreadable or already dead ('$_child_off')"
else
    echo "OK: control setsid'd child pid=$_child_off alive pre-stop"
fi

slinitctl --system stop "$SVC_NOFLAG" >/dev/null 2>&1
_wait_stopped "$SVC_NOFLAG"
sleep 1

# Without the flag, only the main pgroup got SIGTERM; the setsid'd child
# left that pgroup at start time and so should still be alive. If it's
# dead, the probe doesn't actually isolate the flag — a passing sub-case A
# would prove nothing.
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -n "$_child_off" ] && kill -0 "$_child_off" 2>/dev/null; then
    echo "OK: control child $_child_off survives stop (probe isolates flag)"
    kill -KILL "$_child_off" 2>/dev/null || true
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: control child died without flag — probe not discriminating"
fi

test_summary
