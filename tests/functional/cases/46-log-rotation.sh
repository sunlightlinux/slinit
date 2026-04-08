#!/bin/sh
# Test: log rotation creates rotated files when size limit is reached.
# Validates: logfile-max-size, logfile-max-files, rotation mechanics.

wait_for_service "logrot-svc" "STARTED" 10

# Wait for enough output to trigger at least one rotation (1KB max size)
sleep 8

# Check that the main log file exists
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -f /tmp/logrot-svc.log ]; then
    echo "OK: main log file exists"
else
    echo "FAIL: main log file /tmp/logrot-svc.log not found"
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
fi

# Check that at least one rotated file exists (.1 suffix)
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -f /tmp/logrot-svc.log.1 ]; then
    echo "OK: rotated log file .1 exists"
else
    echo "FAIL: rotated log file /tmp/logrot-svc.log.1 not found"
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
fi

# Main log file should be under max-size (1024 bytes, allow some slack)
_TESTS_RUN=$((_TESTS_RUN + 1))
logsize=$(wc -c < /tmp/logrot-svc.log 2>/dev/null || echo 0)
if [ "$logsize" -le 2048 ]; then
    echo "OK: main log file size $logsize <= 2048"
else
    echo "FAIL: main log file size $logsize > 2048 (rotation not working)"
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
fi

# Service should still be running
assert_service_state "logrot-svc" "STARTED" "logrot-svc is STARTED"

test_summary
