#!/bin/sh
# Test: stop-timeout escalates to SIGKILL after timeout.
# Validates: stop-timeout, SIGTERM -> SIGKILL escalation.

wait_for_service "stoptimeout-svc" "STARTED" 10

# The service traps SIGTERM (ignores it). stop-timeout=2 means slinit
# should escalate to SIGKILL after 2 seconds.
pid=$(slinitctl --system status stoptimeout-svc 2>/dev/null | grep 'PID:' | awk '{print $2}')

# Record time before stop
t_start=$(date +%s)

slinitctl --system stop stoptimeout-svc 2>&1
wait_for_service "stoptimeout-svc" "STOPPED" 10

t_end=$(date +%s)
elapsed=$((t_end - t_start))

# Service should be stopped
assert_service_state "stoptimeout-svc" "STOPPED" "stoptimeout-svc is STOPPED"

# Process should be dead
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -n "$pid" ] && ! kill -0 "$pid" 2>/dev/null; then
    echo "OK: process $pid killed after stop-timeout"
else
    echo "FAIL: process $pid still alive after stop"
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
fi

# Elapsed time should be >= 2s (waited for timeout) but < 10s
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$elapsed" -ge 2 ] && [ "$elapsed" -lt 10 ]; then
    echo "OK: stop took ${elapsed}s (expected ~2s for SIGKILL escalation)"
else
    echo "FAIL: stop took ${elapsed}s (expected 2-10s)"
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
fi

test_summary
