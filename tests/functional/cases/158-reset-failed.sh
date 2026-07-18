#!/bin/sh
# Test: slinitctl reset-failed clears the internal startFailed flag.
# The observable side is the CLI's own confirmation output — is-failed
# considers a service failed if either startFailed OR the last exit
# was non-zero, and reset-failed only touches the flag (matching
# systemd semantics), so a stopped-with-exit-7 service remains
# "failed" for is-failed until it's re-started successfully. We check
# the CLI plumbing and the reset --all path here.
_e=0
while [ "$_e" -lt 10 ]; do
    case "$(svc_state rf-fail-svc)" in
        FAILED|STOPPED) break ;;
    esac
    sleep 1; _e=$((_e + 1))
done

# By-name reset must succeed with a success message.
_out=$(slinitctl --system reset-failed rf-fail-svc 2>&1)
_TESTS_RUN=$((_TESTS_RUN + 1))
case "$_out" in
    *"Reset failed state on 'rf-fail-svc'"*)
        echo "OK: reset-failed by-name succeeded"
        ;;
    *)
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: reset-failed by-name: '$_out'"
        ;;
esac

# --all form must also succeed.
_out=$(slinitctl --system reset-failed --all 2>&1)
_TESTS_RUN=$((_TESTS_RUN + 1))
case "$_out" in
    *"Reset failed state on all services"*)
        echo "OK: reset-failed --all succeeded"
        ;;
    *)
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: reset-failed --all: '$_out'"
        ;;
esac

# Missing argument must be an error, not a silent no-op.
if slinitctl --system reset-failed >/dev/null 2>&1; then
    _TESTS_RUN=$((_TESTS_RUN + 1))
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: reset-failed with no arg should error"
else
    _TESTS_RUN=$((_TESTS_RUN + 1))
    echo "OK: reset-failed with no arg refused"
fi

test_summary
