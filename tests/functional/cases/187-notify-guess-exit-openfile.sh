#!/bin/sh
# Test: notify-access + guess-main-pid + exit-type + open-file all
# parse and coexist on one service, and open-file hands the child an
# open descriptor on the file.
#
# notify-access, guess-main-pid and exit-type need fixtures (an
# sd_notify sender, a cgroup) this VM does not have; they are parse
# coverage. open-file is observable: one of the child's fds must point
# at /tmp/187-openfile.log, which slinit created. (The case used to
# check only that the service started.)

wait_for_service "notify-svc" "STARTED" 10
assert_service_state "notify-svc" "STARTED" "notify+guess+exit-type+open-file cluster parses"

pid=$(slinitctl --system status notify-svc 2>/dev/null | awk '/PID:/ {print $2; exit}')
_TESTS_RUN=$((_TESTS_RUN + 1))
_fd=""
for f in /proc/$pid/fd/*; do
    [ "$(readlink "$f")" = "/tmp/187-openfile.log" ] && _fd=${f##*/}
done
if [ -n "$_fd" ]; then
    echo "OK: fd $_fd of the service is /tmp/187-openfile.log"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: no fd of pid $pid points at /tmp/187-openfile.log:"
    ls -l /proc/$pid/fd/ 2>&1 | sed 's/^/    /'
fi

test_summary
