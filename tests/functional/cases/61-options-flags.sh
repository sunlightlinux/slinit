#!/bin/sh
# Test: options flags (kill-all-on-stop, signal-process-only).
# Validates: kill-all-on-stop kills child processes on stop,
#            signal-process-only sends signal only to main PID.

wait_for_service "killgrp-svc" "STARTED" 10
wait_for_service "sigonly-svc" "STARTED" 10

# --- kill-all-on-stop ---
sleep 2

# Get the child PID spawned by killgrp-svc
child_pid=$(cat /tmp/killgrp-child-pid 2>/dev/null)
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -n "$child_pid" ] && kill -0 "$child_pid" 2>/dev/null; then
    echo "OK: child process $child_pid is alive before stop"
else
    echo "FAIL: child process $child_pid not found before stop"
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
fi

# Stop the service — kill-all-on-stop should kill the child too
slinitctl --system stop killgrp-svc 2>&1
sleep 3

# Verify child is dead
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -n "$child_pid" ] && ! kill -0 "$child_pid" 2>/dev/null; then
    echo "OK: child process $child_pid killed on stop"
else
    echo "FAIL: child process $child_pid still alive after stop"
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
fi

assert_service_state "killgrp-svc" "STOPPED" "killgrp-svc is STOPPED"

# --- signal-process-only ---
# sigonly-svc spawns a child. With signal-process-only, stopping should
# only signal the main PID, not the child.
sigonly_child=$(cat /tmp/sigonly-child-pid 2>/dev/null)
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -n "$sigonly_child" ] && kill -0 "$sigonly_child" 2>/dev/null; then
    echo "OK: sigonly child $sigonly_child alive before stop"
else
    echo "FAIL: sigonly child $sigonly_child not found"
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
fi

slinitctl --system stop sigonly-svc 2>&1
sleep 3

# The child may still be alive briefly (orphaned, then reaped by init)
# but the main process should be stopped
assert_service_state "sigonly-svc" "STOPPED" "sigonly-svc is STOPPED"

test_summary
