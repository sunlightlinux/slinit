#!/bin/sh
# Test: working-dir sets the process working directory.
# Validates: WorkingDir config option.

wait_for_service "wd-svc" "STARTED" 10

# The scripted service runs "pwd > /tmp/wd-marker" with working-dir=/tmp
assert_eq "$(cat /tmp/wd-marker 2>/dev/null)" "/tmp" "working directory is /tmp"

test_summary
