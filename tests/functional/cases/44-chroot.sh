#!/bin/sh
# Test: chroot isolates service to a root directory.
# Validates: chroot setting on ProcessService.
# Uses chroot=/ as a safe baseline that works in all VMs.

wait_for_service "chroot-svc" "STARTED" 10

# Give service time to write result
sleep 2

# Service writes marker inside the chroot (which is / here, so /tmp/chroot-result)
result=$(cat /tmp/chroot-result 2>/dev/null)
assert_eq "$result" "chroot-ok" "chroot service ran successfully"

assert_service_state "chroot-svc" "STARTED" "chroot-svc is STARTED"

test_summary
