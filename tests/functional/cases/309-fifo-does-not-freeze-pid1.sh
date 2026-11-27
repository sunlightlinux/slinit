#!/bin/sh
# Test: a FIFO where slinit expects a file does not freeze PID 1.
#
# Up to v4.0.3 slinit opened env-file, log-file and friends with a plain
# blocking open while holding its service lock. A FIFO with nobody on the
# other end blocked that open for ever: PID 1 stopped answering every
# control request (602cbe4..347bd81). Now the opens are non-blocking and
# checked: an env-file that is a FIFO is not read (logged as an error and
# the service starts without it, as for a missing env-file), a log-file
# FIFO with no reader fails the start, and PID 1 keeps answering
# throughout.
# /dev/null stays accepted as an env-file (65c0d2b).

D=/run/309
rm -rf "$D"; mkdir -p "$D"
mkfifo "$D/env.fifo" "$D/log.fifo"

alive() { # -> 0 when PID 1 answers a status request within 5 s
    [ "$(svc_state boot)" = STARTED ]
}
alive || skip_case "PID 1 not answering before the test"

printf '%s\n' 'type = process' 'command = /bin/sleep 3091' 'restart = false' \
    "env-file = $D/env.fifo" > /etc/slinit.d/test-309-env
printf '%s\n' 'type = process' 'command = /bin/sleep 3092' 'restart = false' \
    "logfile = $D/log.fifo" > /etc/slinit.d/test-309-log
printf '%s\n' 'type = process' 'command = /bin/sleep 3093' 'restart = false' \
    'env-file = /dev/null' > /etc/slinit.d/test-309-null

for s in env log; do
    timeout 5 slinitctl --system --no-wait start "test-309-$s" >/dev/null 2>&1
    sleep 2
    _TESTS_RUN=$((_TESTS_RUN + 1))
    if alive; then
        echo "OK: PID 1 still answers after starting a service with a $s FIFO"
    else
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: PID 1 stopped answering after starting a service with a $s FIFO"
        # Let a frozen PID 1 go, so the rest of the case can report.
        timeout 2 sh -c ": > $D/env.fifo" 2>/dev/null
        timeout 2 cat "$D/log.fifo" >/dev/null 2>&1 &
        sleep 1
    fi
done

# A log-file FIFO nobody reads fails the start (ENXIO) instead of
# hanging it.
_st=$(svc_state test-309-log)
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$_st" != STARTED ]; then
    echo "OK: test-309-log did not start on a log FIFO with no reader (state $_st)"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: test-309-log started on a log FIFO with no reader"
fi

slinitctl --system start test-309-null >/dev/null 2>&1
wait_for_service test-309-null STARTED 10
assert_service_state test-309-null STARTED "/dev/null is still a valid env-file"

test_summary
