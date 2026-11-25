#!/bin/sh
# Test: the restart-policy tail parses, and restart-force-exit-status
# restarts a service that exits 42 although restart = no.
# (The case used to check only that the service started.)

wait_for_service "restart-misc-svc" "STARTED" 10
sleep 3

runs=$(wc -l < /run/193-count 2>/dev/null || echo 0)
echo "INFO: exit-42 service ran $runs time(s) with restart = no"
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$runs" -ge 2 ]; then
    echo "OK: restart-force-exit-status restarted it"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: ran $runs time(s); exit 42 did not force a restart"
fi

slinitctl --system --ignore-unstarted stop restart-misc-svc >/dev/null 2>&1

test_summary
