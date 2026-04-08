#!/bin/sh
# Test: close-stdin/close-stdout/close-stderr redirects fds to /dev/null.
# Validates: close-stdin, close-stdout, close-stderr config settings.

wait_for_service "close-fd-svc" "STARTED" 10

# Give the service time to write its fd listing
sleep 2

# The command writes fd listing; with closed fds, 0/1/2 point to /dev/null
# The service may fail to write (stdout closed), so check via /proc directly
pid=$(slinitctl --system status close-fd-svc 2>/dev/null | grep 'PID:' | awk '{print $2}')

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -n "$pid" ] && [ -d "/proc/$pid/fd" ]; then
    # Check that stdin (fd 0) points to /dev/null
    target0=$(readlink /proc/$pid/fd/0 2>/dev/null)
    if [ "$target0" = "/dev/null" ]; then
        echo "OK: stdin (fd 0) -> /dev/null"
    else
        echo "FAIL: stdin (fd 0) -> $target0, expected /dev/null"
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
    fi
else
    echo "FAIL: could not find process PID $pid"
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
fi

# Check stdout (fd 1)
_TESTS_RUN=$((_TESTS_RUN + 1))
target1=$(readlink /proc/$pid/fd/1 2>/dev/null)
if [ "$target1" = "/dev/null" ]; then
    echo "OK: stdout (fd 1) -> /dev/null"
else
    echo "FAIL: stdout (fd 1) -> $target1, expected /dev/null"
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
fi

# Check stderr (fd 2)
_TESTS_RUN=$((_TESTS_RUN + 1))
target2=$(readlink /proc/$pid/fd/2 2>/dev/null)
if [ "$target2" = "/dev/null" ]; then
    echo "OK: stderr (fd 2) -> /dev/null"
else
    echo "FAIL: stderr (fd 2) -> $target2, expected /dev/null"
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
fi

assert_service_state "close-fd-svc" "STARTED" "close-fd-svc is STARTED"

test_summary
