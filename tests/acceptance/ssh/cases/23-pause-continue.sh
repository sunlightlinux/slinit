#!/bin/sh
# 23-pause-continue — `slinitctl pause SVC` sends SIGSTOP to the tracked
# process; `slinitctl continue SVC` sends SIGCONT. Probe via /proc/<pid>/status's
# State: line — 'T' means stopped, 'S' or 'R' means runnable.

SVC="acceptance-test-pause"

trap 'svc_remove "$SVC"' EXIT INT TERM

svc_deploy "$SVC" <<EOF
type = process
command = /bin/sh -c 'while :; do sleep 60; done'
restart = false
EOF

slinitctl --system start "$SVC" >/dev/null 2>&1
wait_for_service "$SVC" "STARTED" 10 || true
assert_service_state "$SVC" "STARTED" "$SVC STARTED"

_pid=$(slinitctl --system status "$SVC" 2>/dev/null | awk '/PID:/ {print $2; exit}')
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -z "$_pid" ]; then
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: no PID for $SVC"
    test_summary
    exit 1
fi
echo "OK: tracking pid $_pid"

# Read /proc state letter — first field after '(comm)' in /proc/<pid>/stat
# is the state char. Using /proc/<pid>/status's State: line is more robust.
proc_state() {
    awk '/^State:/ {print $2; exit}' "/proc/$1/status" 2>/dev/null
}

# Baseline: should be S (sleeping in syscall).
_st0=$(proc_state "$_pid")
echo "INFO: baseline state: $_st0"

# Pause.
slinitctl --system pause "$SVC" >/dev/null 2>&1
# SIGSTOP delivery + scheduler tick.
sleep 1
_st1=$(proc_state "$_pid")
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$_st1" = "T" ]; then
    echo "OK: process in T state after pause"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: expected T after pause, got '$_st1'"
fi

# Continue.
slinitctl --system continue "$SVC" >/dev/null 2>&1
sleep 1
_st2=$(proc_state "$_pid")
_TESTS_RUN=$((_TESTS_RUN + 1))
case "$_st2" in
    S|R)
        echo "OK: process resumed (state '$_st2') after continue"
        ;;
    *)
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: expected S/R after continue, got '$_st2'"
        ;;
esac

test_summary
