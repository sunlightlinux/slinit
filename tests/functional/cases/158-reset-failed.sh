#!/bin/sh
# Test: slinitctl reset-failed makes is-failed stop reporting a failure.
#
# Two kinds of failed start: a process whose binary does not exist and a
# scripted service that exits 3. Each must be is-failed before the reset
# and not after it. The case used to check only the CLI's own
# confirmation text, which slinitctl prints for any loaded service, and
# reset-failed in fact changed nothing is-failed could see.

failed_rc() { slinitctl --system is-failed "$1" >/dev/null 2>&1; echo $?; }

wait_for_service "rf-fail-svc" "STOPPED" 10
slinitctl --system start rf-exit-svc >/dev/null 2>&1

assert_eq "$(failed_rc rf-fail-svc)" "0" "missing binary: is-failed before reset"
assert_eq "$(failed_rc rf-exit-svc)" "0" "exit 3: is-failed before reset"

_out=$(slinitctl --system reset-failed rf-fail-svc 2>&1)
assert_contains "$_out" "Reset failed state on 'rf-fail-svc'" "reset-failed by name confirmed"
assert_eq "$(failed_rc rf-fail-svc)" "1" "missing binary: not failed after reset"
assert_eq "$(failed_rc rf-exit-svc)" "0" "by-name reset left the other service failed"

_out=$(slinitctl --system reset-failed --all 2>&1)
assert_contains "$_out" "Reset failed state on all services" "reset-failed --all confirmed"
assert_eq "$(failed_rc rf-exit-svc)" "1" "exit 3: not failed after reset --all"

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
