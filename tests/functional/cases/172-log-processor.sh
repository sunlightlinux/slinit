#!/bin/sh
# Test: log-processor is invoked on each rotated logfile.
# Validates:
#   1. logfile-max-size triggers rotation.
#   2. The log-processor command runs after each rotation, so its
#      side-effects (appending to /tmp/172-proc.log) accumulate over
#      the observation window.
#
# Semantics under test: pkg/service/logrotate.go's rotate path spawns
# the processor for every rotated file, best-effort — errors are
# logged but do not stop further rotations.

wait_for_service "chatter-svc" "STARTED" 10
assert_service_state "chatter-svc" "STARTED" "chatter-svc STARTED"

# Give the child enough time to blast several rotations. 74-byte line
# + newline = 75 bytes, printed 5x/s → ~375 B/s → rotate threshold hit
# in well under a second. Wait 5s to see multiple rotations.
sleep 5

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -f /tmp/172-proc.log ]; then
    _n=$(wc -l < /tmp/172-proc.log)
    echo "OK: log-processor was invoked $_n time(s)"
else
    echo "FAIL: /tmp/172-proc.log missing — log-processor never ran"
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    test_summary
    return
fi

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$_n" -ge 2 ]; then
    echo "OK: multiple rotations processed ($_n runs)"
else
    echo "FAIL: only $_n rotation(s) processed — expected several within 5s"
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
fi

test_summary
