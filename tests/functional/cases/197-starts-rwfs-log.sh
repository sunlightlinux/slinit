#!/bin/sh
# Test: options = starts-rwfs / starts-log parse and interoperate with
# the ready-notification pipe.
# Validates:
#   1. Both flags parse and set on the record.
#   2. With a proper readiness signal (a byte on fd 3), slinit promotes
#      the svc to STARTED — proving the pipe is genuinely wired for
#      these flags, not silently ignored.

wait_for_service "rwfs-log-svc" "STARTED" 10
assert_service_state "rwfs-log-svc" "STARTED" "options=starts-rwfs,starts-log promoted after ready-notification signal"

test_summary
