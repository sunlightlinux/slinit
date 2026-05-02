#!/bin/sh
# Test: bgprocess service type reads PID from pid-file.
# Validates: bgprocess type, pid-file reading, daemon tracking.

wait_for_service "bg-svc" "STARTED" 15

# Verify the PID file was created
assert_eq "$(test -f /tmp/bg-svc.pid && echo yes || echo no)" "yes" \
    "pid-file /tmp/bg-svc.pid exists"

# Read the PID from the file
file_pid=$(cat /tmp/bg-svc.pid 2>/dev/null | tr -d '[:space:]')

# Verify the PID is a number
_TESTS_RUN=$((_TESTS_RUN + 1))
case "$file_pid" in
    *[!0-9]*)
        echo "FAIL: pid-file contains non-numeric: '$file_pid'"
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        ;;
    "")
        echo "FAIL: pid-file is empty"
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        ;;
    *)
        echo "OK: pid-file contains numeric PID: $file_pid"
        ;;
esac

# Verify slinit tracked the PID
status_pid=$(slinitctl --system status bg-svc 2>/dev/null | grep 'PID:' | awk '{print $2}')
assert_eq "$status_pid" "$file_pid" "slinit tracks PID from pid-file"

# Verify the process is actually running
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -n "$file_pid" ] && kill -0 "$file_pid" 2>/dev/null; then
    echo "OK: background process $file_pid is alive"
else
    echo "FAIL: background process $file_pid is not running"
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
fi

assert_service_state "bg-svc" "STARTED" "bg-svc is STARTED"

# Stop the service — slinit should kill the tracked PID
slinitctl --system stop bg-svc 2>&1
sleep 3

assert_service_state "bg-svc" "STOPPED" "bg-svc is STOPPED after stop"

test_summary
