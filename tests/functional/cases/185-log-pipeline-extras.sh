#!/bin/sh
# Test: log-pipeline extras — perms/uid/gid/rotate-time on one svc,
# forwarder + filter cluster on another.
# Validates: both services parse + reach STARTED; logfile-permissions
# lands on the file (spot-checked via stat).

wait_for_service "logfmt-svc" "STARTED" 10
wait_for_service "logfilter-svc" "STARTED" 10
assert_service_state "logfmt-svc" "STARTED" "logfile perms/uid/gid/rotate cluster parses"
assert_service_state "logfilter-svc" "STARTED" "log-forward-* + log-level-max + log-sanitize* cluster parses"

# Give the writer a moment to open the logfile.
sleep 1

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -f /tmp/185-log.log ]; then
    _mode=$(stat -c '%a' /tmp/185-log.log 2>/dev/null)
    if [ "$_mode" = "640" ]; then
        echo "OK: logfile-permissions = 0640 landed on /tmp/185-log.log"
    else
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: /tmp/185-log.log mode is $_mode, expected 640"
    fi
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: /tmp/185-log.log never created"
fi

test_summary
