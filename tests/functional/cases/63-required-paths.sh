#!/bin/sh
# Test: required-files and required-dirs pre-start guards.
# Validates: required-files, required-dirs existence checks before exec.

wait_for_service "reqok-svc" "STARTED" 10

# reqok-svc requires /etc/hostname and /tmp — both exist, should start fine
assert_service_state "reqok-svc" "STARTED" "reqok-svc STARTED (required paths exist)"

# reqfail-svc requires a nonexistent file — should fail to start
slinitctl --system start reqfail-svc 2>&1 || true
sleep 3

# The service should be STOPPED or failed (not STARTED)
_state=$(slinitctl --system status reqfail-svc 2>/dev/null | grep 'State:' | awk '{print $2}')
_TESTS_RUN=$((_TESTS_RUN + 1))
case "$_state" in
    STOPPED|"")
        echo "OK: reqfail-svc did not start (state: ${_state:-not loaded})"
        ;;
    STARTED)
        echo "FAIL: reqfail-svc should not have started (missing required file)"
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        ;;
    *)
        echo "OK: reqfail-svc in state '$_state' (not STARTED)"
        ;;
esac

# Verify is-failed reports failure
assert_exit_code "slinitctl --system is-failed reqfail-svc" 0 \
    "reqfail-svc is-failed exits 0"

test_summary
