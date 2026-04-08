#!/bin/sh
# Test: new-session creates a new session (setsid) for the service process.
# Validates: new-session = true, setsid() call, SID == PID.

wait_for_service "sess-svc" "STARTED" 10

# Give service time to write results
sleep 2

# In a new session, the session ID should equal the process PID
# (the process becomes the session leader)
sid=$(cat /tmp/sess-sid 2>/dev/null)
pid=$(cat /tmp/sess-pid 2>/dev/null)

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -n "$sid" ] && [ -n "$pid" ] && [ "$sid" = "$pid" ]; then
    echo "OK: new session — SID ($sid) == PID ($pid)"
else
    echo "FAIL: SID ($sid) != PID ($pid) — setsid not effective"
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
fi

assert_service_state "sess-svc" "STARTED" "sess-svc is STARTED"

test_summary
