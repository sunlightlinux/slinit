#!/bin/sh
# Test: log rotation creates rotated files when size limit is reached.
# Validates: logfile-max-size, logfile-max-files, rotation mechanics.
# Note: rotated files use timestamp suffix (logrot-svc.log.YYYYMMDD-HHMMSS).

wait_for_service "logrot-svc" "STARTED" 10

# yes(1) produces output at max speed — rotation triggers almost instantly
sleep 5

# Count all log files (main + rotated) — with fast rotation the main file
# may be mid-rename at check time, so we count everything matching the prefix
all_count=$(ls /tmp/logrot-svc.log* 2>/dev/null | wc -l)
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$all_count" -gt 0 ]; then
    echo "OK: log files exist ($all_count total)"
else
    echo "FAIL: no log files found at /tmp/logrot-svc.log*"
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
