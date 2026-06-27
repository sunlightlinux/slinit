#!/bin/sh
# 60-options-signal-process-only — verify the flag changes the
# stop-signal target from the process group to the single PID.
# process.go:1086 calls SignalProcess(pid, sig, SignalProcessOnly):
# false → kill(-pid, sig) reaches every PID sharing the service's
# pgroup; true → kill(pid, sig) only hits the main shell.
#
# Topology: a child sleep is backgrounded inside the same shell with
# plain `&` (no setsid). It inherits the parent's pgroup, so the
# default codepath hits it via the pgroup signal. With the flag set,
# only the main shell receives SIGTERM; the child sleep is reparented
# to PID 1 and keeps running until its natural timeout.
#
# Sub-case A: flag set     → backgrounded child SURVIVES stop.
# Sub-case B: default       → backgrounded child DIES on stop. Without
# this inverse, A passing wouldn't prove the flag — a topology bug
# where the child accidentally escaped the pgroup would look the same.

SVC_FLAG="acceptance-test-sigonly-on"
SVC_NOFLAG="acceptance-test-sigonly-off"
PIDFILE_ON="/run/acceptance-sigonly-on.pid"
PIDFILE_OFF="/run/acceptance-sigonly-off.pid"

cleanup() {
    svc_remove "$SVC_FLAG" "$SVC_NOFLAG"
    for _f in "$PIDFILE_ON" "$PIDFILE_OFF"; do
        _p=$(cat "$_f" 2>/dev/null)
        case "$_p" in
            ""|*[!0-9]*) ;;
            *) kill -KILL "$_p" 2>/dev/null || true ;;
        esac
        rm -f "$_f"
    done
}
trap cleanup EXIT INT TERM

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

# --- Sub-case A: signal-process-only = yes -------------------------------
rm -f "$PIDFILE_ON"
svc_deploy "$SVC_FLAG" <<EOF
type = process
options = signal-process-only
command = /bin/sh -c 'sleep 3600 & echo \$! > $PIDFILE_ON; while :; do sleep 60; done'
restart = false
EOF

slinitctl --system start "$SVC_FLAG" >/dev/null 2>&1
wait_for_service "$SVC_FLAG" "STARTED" 10 || true
assert_service_state "$SVC_FLAG" "STARTED" "$SVC_FLAG STARTED"

_child_on=$(_read_child_pid "$PIDFILE_ON")
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -z "$_child_on" ] || ! kill -0 "$_child_on" 2>/dev/null; then
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: sigonly child pid unreadable or already dead ('$_child_on')"
else
    echo "OK: sigonly child pid=$_child_on alive pre-stop"
fi

slinitctl --system stop "$SVC_FLAG" >/dev/null 2>&1
_wait_stopped "$SVC_FLAG"
# Brief settle: the main shell's death is async; we want a stable read
# of the child's state, not a race against reparenting.
sleep 1

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -n "$_child_on" ] && kill -0 "$_child_on" 2>/dev/null; then
    echo "OK: sigonly child $_child_on survives stop (only main pid signalled)"
    kill -KILL "$_child_on" 2>/dev/null || true
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: sigonly child died — flag did not gate the pgroup signal"
fi

svc_remove "$SVC_FLAG"

# --- Sub-case B: control (no signal-process-only) ------------------------
rm -f "$PIDFILE_OFF"
svc_deploy "$SVC_NOFLAG" <<EOF
type = process
command = /bin/sh -c 'sleep 3600 & echo \$! > $PIDFILE_OFF; while :; do sleep 60; done'
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
    echo "OK: control child pid=$_child_off alive pre-stop"
fi

slinitctl --system stop "$SVC_NOFLAG" >/dev/null 2>&1
_wait_stopped "$SVC_NOFLAG"
sleep 1

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -n "$_child_off" ] && kill -0 "$_child_off" 2>/dev/null; then
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: control child $_child_off survived without the flag — pgroup signal missed it"
    kill -KILL "$_child_off" 2>/dev/null || true
else
    echo "OK: control child reaped via pgroup signal (probe isolates flag)"
fi

test_summary
