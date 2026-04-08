#!/bin/sh
# Test: new-session creates a new session (setsid) for the service process.
# Validates: new-session = true, setsid() call, SID == PID.

wait_for_service "sess-svc" "STARTED" 10

# Get PID from slinitctl
pid=$(slinitctl --system status sess-svc 2>/dev/null | grep 'PID:' | awk '{print $2}')

# Read session ID from /proc — parse stat carefully (field 2 has parens)
# Format: pid (comm) state ppid pgrp session ...
# Strip through closing paren, then field 4 is session
sid=$(sed 's/.*) //' /proc/$pid/stat 2>/dev/null | cut -d' ' -f4)

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -n "$sid" ] && [ -n "$pid" ] && [ "$sid" = "$pid" ]; then
    echo "OK: new session — SID ($sid) == PID ($pid)"
else
    echo "FAIL: SID ($sid) != PID ($pid) — setsid not effective"
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
fi

assert_service_state "sess-svc" "STARTED" "sess-svc is STARTED"

test_summary
