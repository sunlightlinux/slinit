#!/bin/sh
# Test: restart-randomized-delay parses cleanly and doesn't wedge
# the start path. Full jitter distribution measurement requires
# many restarts + statistics — not fit-for-CI. Parse+reach-STARTED
# regression is what this guards.
wait_for_service "rrd-svc" "STARTED" 10
assert_service_state "rrd-svc" "STARTED" "svc with restart-randomized-delay reached STARTED"

_pid=$(slinitctl --system status rrd-svc 2>/dev/null | awk '/PID:/ {print $2; exit}')
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -n "$_pid" ] && [ "$_pid" != "0" ] && [ -d "/proc/$_pid" ]; then
    echo "OK: live PID=$_pid — parser + backoff wiring OK"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: no live PID (got '$_pid')"
fi

test_summary
