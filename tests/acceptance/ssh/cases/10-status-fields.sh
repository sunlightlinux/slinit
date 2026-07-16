#!/bin/sh
# 10-status-fields — `slinitctl status` prints State/Target/Type/PID/Exit
# for a live service, and PID disappears while Exit surfaces after a
# non-zero termination. Every other case pulls only `State:` out via
# svc_state, so this is the direct end-to-end check that the human-
# facing formatter emits the other fields correctly.

SVC_PROC="acceptance-test-status-fields-proc"
SVC_FAIL="acceptance-test-status-fields-fail"

cleanup() { svc_remove "$SVC_PROC" "$SVC_FAIL"; }
trap cleanup EXIT INT TERM

svc_deploy "$SVC_PROC" <<EOF
type = process
command = /bin/sh -c 'exec sleep 600'
restart = false
EOF

svc_deploy "$SVC_FAIL" <<EOF
type = process
command = /bin/sh -c 'exit 7'
restart = false
EOF

# ---- Started process service --------------------------------------------
slinitctl --system start "$SVC_PROC" >/dev/null 2>&1
wait_for_service "$SVC_PROC" "STARTED" 10 || true
_out_up=$(slinitctl --system status "$SVC_PROC" 2>/dev/null)

assert_contains "$_out_up" "State:   STARTED" "state line present"
# formatTarget emits the *verb* (start/stop), not the state name.
assert_contains "$_out_up" "Target:  start" "target line reports 'start'"
assert_contains "$_out_up" "Type:    process" "type line reports 'process'"

_pid_line=$(printf '%s\n' "$_out_up" | awk '/^  PID:/ {print $2; exit}')
_TESTS_RUN=$((_TESTS_RUN + 1))
case "$_pid_line" in
    [1-9]*)
        # Sanity check: /proc/<pid> exists and is a directory.
        if [ -d "/proc/$_pid_line" ]; then
            echo "OK: PID line reports live pid $_pid_line"
        else
            _TESTS_FAILED=$((_TESTS_FAILED + 1))
            echo "FAIL: PID $_pid_line reported but /proc/$_pid_line missing"
        fi
        ;;
    *)
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: no numeric PID line in status; got '$_pid_line'"
        ;;
esac

# ---- Stopped process service --------------------------------------------
slinitctl --system stop "$SVC_PROC" >/dev/null 2>&1
_e=0
while [ "$_e" -lt 8 ]; do
    [ "$(svc_state "$SVC_PROC")" = "STOPPED" ] && break
    sleep 1; _e=$((_e + 1))
done
_out_down=$(slinitctl --system status "$SVC_PROC" 2>/dev/null)

assert_contains "$_out_down" "State:   STOPPED" "stopped state line"
assert_contains "$_out_down" "Target:  stop" "target line reports 'stop' after stop"
assert_contains "$_out_down" "Type:    process" "type still reported when stopped"
assert_not_contains "$_out_down" "PID:" "PID line absent when service is stopped"

# ---- Failed process service --------------------------------------------
# NB: only ProcessService.exitStatus is populated on failure — the base
# ServiceRecord.GetExitStatus() returns a zero value, so a failing
# ScriptedService never surfaces its exit code via `Exit:` in status.
# Drive the failure through a `type = process` command so the Exit
# line materialises.
slinitctl --system start "$SVC_FAIL" >/dev/null 2>&1 || true
_e=0
while [ "$_e" -lt 10 ]; do
    case "$(svc_state "$SVC_FAIL")" in
        FAILED|STOPPED) break ;;
    esac
    sleep 1; _e=$((_e + 1))
done
_out_fail=$(slinitctl --system status "$SVC_FAIL" 2>/dev/null)

assert_contains "$_out_fail" "Type:    process" "failure svc type=process"
assert_contains "$_out_fail" "Exit:    7" "Exit line surfaces exit-code 7"

test_summary
