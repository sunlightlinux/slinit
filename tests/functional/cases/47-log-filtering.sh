#!/bin/sh
# Test: log-include and log-exclude filter output written to logfile.
# Validates: log-include regex, log-exclude regex, filtering pipeline.
#
# The service prints INFO/DEBUG/ERROR lines numbered 0, 1, 2, ...
# include keeps INFO and ERROR; exclude drops odd-numbered ERROR lines.
# (The exclude used to be ^DEBUG:, which the include had already
# dropped, so removing it changed nothing.)

wait_for_service "logfilt-svc" "STARTED" 10
sleep 4

log=/tmp/logfilt-svc.log
count() { grep -c "$1" "$log" 2>/dev/null; }

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$(count '^INFO: message 3$')" = "1" ] && [ "$(count '^ERROR: problem 2$')" = "1" ]; then
    echo "OK: INFO lines and even ERROR lines kept"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: expected 'INFO: message 3' and 'ERROR: problem 2' in the log:"
    head -12 "$log" 2>/dev/null
fi

assert_eq "$(count '^DEBUG:')" "0" "log-include dropped DEBUG lines"
assert_eq "$(count '^ERROR: problem [13579]$')" "0" "log-exclude dropped odd ERROR lines"

assert_service_state "logfilt-svc" "STARTED" "logfilt-svc is STARTED"

test_summary
