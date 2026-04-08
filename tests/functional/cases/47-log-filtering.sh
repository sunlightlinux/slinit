#!/bin/sh
# Test: log-include and log-exclude filter output written to logfile.
# Validates: log-include regex, log-exclude regex, filtering pipeline.

wait_for_service "logfilt-svc" "STARTED" 10

# Wait for several rounds of output
sleep 5

# Log file should contain INFO lines
logcontent=$(cat /tmp/logfilt-svc.log 2>/dev/null)
assert_contains "$logcontent" "INFO:" "log contains INFO lines"

# Log file should contain ERROR lines
assert_contains "$logcontent" "ERROR:" "log contains ERROR lines"

# Log file should NOT contain DEBUG lines (excluded)
assert_not_contains "$logcontent" "DEBUG:" "log excludes DEBUG lines"

# Service should still be running
assert_service_state "logfilt-svc" "STARTED" "logfilt-svc is STARTED"

test_summary
