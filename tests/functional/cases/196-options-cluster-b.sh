#!/bin/sh
# Test: options = runs-on-console gives the service the console as its
# stdio; a control without the option gets /dev/null. (The case used to
# check only that the service started.)

wait_for_service "optsb-svc" "STARTED" 10
wait_for_service "optsb-ctl" "STARTED" 10
assert_service_state "optsb-svc" "STARTED" "options=runs-on-console parses + service starts"

fd1_of() {
    _pid=$(slinitctl --system status "$1" 2>/dev/null | awk '/PID:/ {print $2; exit}')
    readlink "/proc/$_pid/fd/1" 2>/dev/null
}
echo "INFO: runs-on-console fd1=$(fd1_of optsb-svc), control fd1=$(fd1_of optsb-ctl)"
assert_eq "$(fd1_of optsb-svc)" "/dev/console" "runs-on-console: stdout is the console"
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$(fd1_of optsb-ctl)" != "/dev/console" ]; then
    echo "OK: control without the option is not on the console"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: control is on the console too; the check cannot tell"
fi

test_summary
