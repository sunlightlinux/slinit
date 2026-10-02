#!/bin/sh
# Test: pre-start-command sequencing.
# Validates:
#   1. pre-start-command runs SYNCHRONOUSLY before the main command,
#      so its side-effects (a marker file) are observable by the time
#      the service reaches STARTED.
#   2. A pre-start-command that exits non-zero blocks the main command
#      from running at all, and the service does NOT reach STARTED.
#
# Semantics under test (pkg/service/process.go runOneShotUp): the hook
# is called before startProcess(); a non-zero return short-circuits the
# whole BringUp and reports a start failure.

wait_for_service "prehook-svc" "STARTED" 10

# Printed on every run, pass or fail. This case fails on the CI runner and
# passes here under every condition tried — fresh image, software
# emulation, and a single starved core — so what is missing is not another
# theory but the service's own account of itself at the moment of the
# check. A passing run's dump is the baseline the failing one is read
# against, which is why this is not conditional.
echo "--- diag: prehook-svc at the moment of the check ---"
slinitctl status prehook-svc 2>&1 || true
echo "--- diag: end ---"

assert_service_state "prehook-svc" "STARTED" "prehook-svc reaches STARTED"

# Marker written by the hook — must already exist by the time the svc
# is STARTED. If pre-start ran async after the main command, this file
# would still be missing here.
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -f /tmp/167-marker ]; then
    echo "OK: pre-start marker present before main command ran"
else
    echo "FAIL: pre-start marker /tmp/167-marker missing after STARTED"
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
fi

# Give the failing service a moment to attempt + fail its hook.
sleep 2

# The failing pre-hook must have blocked the main command. If the main
# command had run, it would have written /tmp/167-not-run.
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ ! -f /tmp/167-not-run ]; then
    echo "OK: main command was NOT spawned after pre-start failure"
else
    echo "FAIL: /tmp/167-not-run exists — main command ran despite pre-start failure"
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
fi

# And the service must not be STARTED.
_state=$(slinitctl --system status prehook-fail-svc 2>/dev/null | awk '/State:/ { print $2; exit }')
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$_state" != "STARTED" ]; then
    echo "OK: prehook-fail-svc not STARTED (state: $_state)"
else
    echo "FAIL: prehook-fail-svc should not be STARTED after failing pre-hook"
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
fi

test_summary
