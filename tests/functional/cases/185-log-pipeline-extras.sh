#!/bin/sh
# Test: log-pipeline extras — perms/uid/gid/rotate-time on one svc,
# forwarder + filter cluster on another.
# Validates: both services parse + reach STARTED; logfile-permissions
# lands on the file, log-line-prefix on its lines, and log-level-max
# drops the line above it. (The case used to check only the mode.)

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

# log-line-prefix lands on every line of logfmt-svc (a trailing space
# is added to it).
echo "INFO: first line of /tmp/185-log.log: '$(head -1 /tmp/185-log.log 2>/dev/null)'"
assert_contains "$(head -1 /tmp/185-log.log 2>/dev/null)" "[svc] message" \
    "log-line-prefix prepended to the line"

# log-level-max = notice: <5> kept, <7> dropped.
_e=0
while [ "$_e" -lt 10 ] && ! grep -q notice-line /tmp/185-filter.log 2>/dev/null; do
    sleep 0.5; _e=$((_e + 1))
done
assert_eq "$(grep -c notice-line /tmp/185-filter.log 2>/dev/null)" "1" "log-level-max kept the <5> line"
assert_eq "$(grep -c debug-line /tmp/185-filter.log 2>/dev/null)" "0" "log-level-max dropped the <7> line"

test_summary
