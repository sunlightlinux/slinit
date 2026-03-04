#!/bin/sh
# Test: send signal to a running service process.
# Validates: CmdSignal, signal delivery, process stays alive after non-fatal signal.

wait_for_service "sig-svc" "STARTED" 10

# Send SIGUSR1 (non-fatal, trapped by the service)
output=$(slinitctl --system signal USR1 sig-svc 2>&1)
assert_contains "$output" "sent" "signal command succeeded"

# Service should still be running
sleep 1
assert_service_state "sig-svc" "STARTED" "sig-svc still STARTED after USR1"

# Verify signal was received (service writes marker on USR1)
sleep 1
assert_eq "$(cat /tmp/sig-received 2>/dev/null)" "yes" "service received USR1"

test_summary
