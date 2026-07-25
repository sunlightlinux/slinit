#!/bin/sh
# Test: ioprio directive sets the process I/O scheduling priority.
# Validates:
#   1. The child process runs with the class:level requested by
#      `ioprio = be:7` — verifiable via busybox `ionice -p PID`.
#   2. Distinguishes from the kernel default (be:4 or nice-derived),
#      so a passing test proves the syscall actually fired.
#
# Semantics under test: pkg/process/attrs.go calls the raw
# SYS_ioprio_set syscall with the encoded class<<13 | prio value
# before exec.

wait_for_service "ioprio-svc" "STARTED" 10

_pid=$(slinitctl --system status ioprio-svc 2>/dev/null | awk '/PID:/ { print $2; exit }')
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -z "$_pid" ] || [ "$_pid" = "0" ]; then
    echo "FAIL: could not resolve pid for ioprio-svc"
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    test_summary
    return
fi
echo "OK: ioprio-svc pid=$_pid"

# busybox ionice -p prints e.g. "best-effort: prio 7"
_ionice=$(ionice -p "$_pid" 2>/dev/null)

_TESTS_RUN=$((_TESTS_RUN + 1))
if echo "$_ionice" | grep -qi 'best-effort'; then
    echo "OK: ioprio class = best-effort"
else
    echo "FAIL: ioprio class not best-effort (ionice: $_ionice)"
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
fi

_TESTS_RUN=$((_TESTS_RUN + 1))
if echo "$_ionice" | grep -qE 'prio\s*7|:\s*7'; then
    echo "OK: ioprio priority = 7 (distinct from default)"
else
    echo "FAIL: ioprio priority is not 7 (ionice: $_ionice)"
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
fi

test_summary
