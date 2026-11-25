#!/bin/sh
# Test: options-flags cluster A (unmask-intr / skippable /
# start-interruptible) round-trips through the parser and boot graph,
# and unmask-intr takes effect.
#
# The service also runs on the console. With unmask-intr slinit makes
# the console its controlling terminal, so field 7 (tty_nr) of
# /proc/PID/stat is non-zero; without it the child has no controlling
# terminal and tty_nr is 0. start-interruptible is observed in case
# 249; skippable is parse coverage. (The case used to check only that
# the service started.)

wait_for_service "optsa-svc" "STARTED" 10
assert_service_state "optsa-svc" "STARTED" "options=unmask-intr,skippable,start-interruptible parses"

pid=$(slinitctl --system status optsa-svc 2>/dev/null | awk '/PID:/ {print $2; exit}')
tty_nr=$(awk '{ print $7 }' /proc/$pid/stat 2>/dev/null)
echo "INFO: pid $pid tty_nr=$tty_nr"
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -n "$tty_nr" ] && [ "$tty_nr" != 0 ]; then
    echo "OK: unmask-intr gave the service a controlling terminal"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: service has no controlling terminal (tty_nr=$tty_nr)"
fi

test_summary
