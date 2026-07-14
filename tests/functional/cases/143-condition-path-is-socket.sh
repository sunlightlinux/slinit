#!/bin/sh
# Test: condition-path-is-socket = <path> checks S_ISSOCK.
# Positive case points at /run/slinit.socket — a real AF_UNIX
# socket slinit itself creates at boot. Negative case points at
# /etc/os-release — a regular file. Only the positive service
# should end up with a live PID; the negative is skipped.

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ ! -S /run/slinit.socket ]; then
    echo "SKIP: /run/slinit.socket not present (test env)"
    test_summary
    return 0
fi
echo "OK: control socket present as expected"

wait_for_service "sock-yes" "STARTED" 10
wait_for_service "sock-no"  "STARTED" 10

# Both land in STARTED — silent skip on condition fail.
assert_service_state "sock-yes" "STARTED" "sock-yes reached STARTED"
assert_service_state "sock-no"  "STARTED" "sock-no reached STARTED"

_pid_yes=$(slinitctl --system status sock-yes 2>/dev/null | awk '/PID:/ { print $2; exit }')
_pid_no=$(slinitctl --system status sock-no  2>/dev/null | awk '/PID:/ { print $2; exit }')

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -n "$_pid_yes" ] && [ "$_pid_yes" != "0" ] && [ "$_pid_yes" != "-" ] && [ -d "/proc/$_pid_yes" ]; then
    echo "OK: sock-yes has live PID=$_pid_yes (condition matched)"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: sock-yes has no live PID (got '$_pid_yes')"
fi

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -z "$_pid_no" ] || [ "$_pid_no" = "0" ] || [ "$_pid_no" = "-" ]; then
    echo "OK: sock-no skipped — no PID (condition failed on regular file)"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: sock-no has PID=$_pid_no — condition should have failed"
fi

test_summary
