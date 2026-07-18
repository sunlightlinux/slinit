#!/bin/sh
# Test: refuse-manual-start blocks a direct `slinitctl start` after
# the service has been stopped; refuse-manual-stop blocks a direct
# `slinitctl stop` while it's running. --force overrides the stop.
wait_for_service "refuse-svc" "STARTED" 10

# Try stop: should be refused.
_out=$(slinitctl --system stop refuse-svc 2>&1)
_TESTS_RUN=$((_TESTS_RUN + 1))
case "$_out" in
    *"refuses manual stop"*)
        echo "OK: refuse-manual-stop rejected slinitctl stop"
        ;;
    *)
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: refuse-manual-stop did not reject: '$_out'"
        ;;
esac
assert_service_state "refuse-svc" "STARTED" "still running despite stop attempt"

# --force must override
slinitctl --system --force stop refuse-svc >/dev/null 2>&1
wait_for_service "refuse-svc" "STOPPED" 10
assert_service_state "refuse-svc" "STOPPED" "--force overrode refuse-manual-stop"

# Now try to start it manually: should be refused.
_out=$(slinitctl --system start refuse-svc 2>&1)
_TESTS_RUN=$((_TESTS_RUN + 1))
case "$_out" in
    *"refuses manual start"*)
        echo "OK: refuse-manual-start rejected slinitctl start"
        ;;
    *)
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: refuse-manual-start did not reject: '$_out'"
        ;;
esac

test_summary
