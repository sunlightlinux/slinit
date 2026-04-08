#!/bin/sh
# Test: log rotation creates rotated files when size limit is reached.
# Validates: logfile-max-size, logfile-max-files, rotation mechanics.
# Note: rotated files use timestamp suffix (logrot-svc.log.YYYYMMDD-HHMMSS).

wait_for_service "logrot-svc" "STARTED" 10

# yes(1) produces output at max speed — rotation triggers almost instantly
sleep 5

# Check that the main log file exists
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -f /tmp/logrot-svc.log ]; then
    echo "OK: main log file exists"
else
    echo "FAIL: main log file /tmp/logrot-svc.log not found"
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
fi

# Check that at least one rotated file exists (timestamp suffix pattern)
rotated_count=$(ls /tmp/logrot-svc.log.* 2>/dev/null | wc -l)
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$rotated_count" -gt 0 ]; then
    echo "OK: $rotated_count rotated log file(s) found"
else
    echo "FAIL: no rotated log files found (expected logrot-svc.log.YYYYMMDD-HHMMSS)"
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
fi

# Verify max-files limit: should have at most 3 rotated files
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$rotated_count" -le 3 ]; then
    echo "OK: rotated file count $rotated_count <= 3 (max-files enforced)"
else
    echo "FAIL: rotated file count $rotated_count > 3 (max-files=3 not enforced)"
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
fi

# Service should still be running
assert_service_state "logrot-svc" "STARTED" "logrot-svc is STARTED"

test_summary
