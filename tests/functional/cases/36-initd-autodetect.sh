#!/bin/sh
# Test: auto-detect /etc/init.d scripts as scripted services.
# Validates: init.d fallback in loader, LSB header parsing, start/stop.

wait_for_service "boot" "STARTED" 10

# Start the init.d service (not in slinit.d, only in /etc/init.d)
output=$(slinitctl --system start initd-demo 2>&1)
sleep 2

# Verify the service ran its start command
assert_eq "$(cat /tmp/initd-demo-status 2>/dev/null)" "initd-demo: started" \
    "init.d start command executed"

# Verify slinit sees it as a service
output=$(slinitctl --system list 2>&1)
assert_contains "$output" "initd-demo" "init.d service appears in list"

# Stop the service
slinitctl --system stop initd-demo 2>&1
sleep 1

# Verify stop command ran
assert_eq "$(cat /tmp/initd-demo-status 2>/dev/null)" "initd-demo: stopped" \
    "init.d stop command executed"

test_summary
