#!/bin/sh
# Test: job-timeout-sec fires against a start job stuck waiting on
# a dep that never reaches STARTED.
_start_out=$(slinitctl --system start jt-svc 2>&1)
sleep 5
_status=$(slinitctl --system status jt-svc 2>&1)
_st=$(echo "$_status" | awk '/State:/ {print $2; exit}')

_TESTS_RUN=$((_TESTS_RUN + 1))
case "$_st" in
    FAILED|STOPPED)
        echo "OK: jt-svc left STARTING within job-timeout window (state=$_st)"
        ;;
    STARTING)
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: jt-svc still STARTING after 5s"
        ;;
    *)
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        # Bake diagnostic context into the FAIL line so it survives
        # run-tests.sh's `grep ^FAIL:` filter.
        _diag=$(printf '%s' "$_start_out $_status" | tr '\n' '|' | cut -c1-200)
        echo "FAIL: jt-svc unexpected state '$_st' | start='$_start_out' | status='$_diag'"
        ;;
esac
test_summary
