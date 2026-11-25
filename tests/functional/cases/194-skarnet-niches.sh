#!/bin/sh
# Test: skarnet-inspired niches (alert-file/-level, pass-cs-fd,
# utmp-mode) parse and coexist, and the alert channel routes by level.
#
# The service prints one line each at syslog priority 3, 4 and 6. The
# main logfile gets all three; the alert file only the <3> one, because
# alert-level = err. With the default (warn) the <4> line would be there
# too, and without alert-file there would be no file at all. pass-cs-fd
# is observed in case 268; utmp-mode is parse coverage. (The case used
# to check only that the service started.)

wait_for_service "skarnet-svc" "STARTED" 10
assert_service_state "skarnet-svc" "STARTED" "skarnet-niches cluster parses + service starts"

_e=0
while [ "$_e" -lt 20 ] && [ "$(grep -c disk /tmp/194-main.log 2>/dev/null)" != 3 ]; do
    sleep 0.25; _e=$((_e + 1))
done
assert_eq "$(grep -c disk /tmp/194-main.log 2>/dev/null)" "3" "main log got all three lines"
assert_eq "$(grep -c 'disk on fire' /tmp/194-alert.log 2>/dev/null)" "1" "alert file got the <3> line"
assert_eq "$(grep -c -e 'disk warm' -e 'disk fine' /tmp/194-alert.log 2>/dev/null)" "0" "alert file has no <4>/<6> line"

test_summary
