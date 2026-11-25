#!/bin/sh
# 254-pidfile-pid1-refused: a pid-file naming PID 1 is refused. slinit used to
# adopt init itself as the daemon — and a later stop sent SIGTERM to PID 1,
# which is a reboot.

slinitctl --system start badpid > /dev/null 2>&1
sleep 2
st=$(svc_state badpid)
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$st" = "STARTED" ]; then
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: service adopted PID 1 from its pid-file (state STARTED)"
else
    echo "OK: pid-file naming PID 1 refused (state $st)"
fi
slinitctl --system stop badpid > /dev/null 2>&1
sleep 1
assert_service_state "boot" "STARTED" "PID 1 still running after the stop"

test_summary
