#!/bin/sh
# Test: 'once' command starts a service without auto-restart.
# Validates: once sets restart=never, service does not restart after exit.

# Start the service with 'once' (overrides restart=true in config)
output=$(slinitctl --system once once-svc 2>&1)
wait_for_service "once-svc" "STARTED" 10
assert_service_state "once-svc" "STARTED" "once-svc started via once"

# Service exits with code 1 after 2s — should NOT restart
sleep 5

# Should be STOPPED, not STARTED (no auto-restart)
assert_service_state "once-svc" "STOPPED" "once-svc stopped without restart"

test_summary
