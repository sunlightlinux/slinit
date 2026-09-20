#!/bin/sh
# nosystemd checklist — systemd#1312 and systemd#6478.
#
# #1312 "restarting systemd service on dependency failure": a service
# whose hard dependency failed was still restarted in a loop instead of
# being left down.
# #6478 "systemctl should not consider active->failed as a successful
# operation": the CLI reported success for a start that ended in
# failure, so scripts could not tell the two apart.
# https://github.com/systemd/systemd/issues/1312
# https://github.com/systemd/systemd/issues/6478
#
# slinit: a failed hard dependency must leave the dependent DOWN, must
# not spin it under restart=true, and `slinitctl start` must exit
# non-zero so a shell script sees the failure.

# depfail-base runs /bin/false, so it can never come up.
slinitctl start depfail-base >/dev/null 2>&1
sleep 2

# #6478: the exit status has to reflect the outcome, not the fact that
# the request was accepted.
assert_exit_code "slinitctl start depfail-user" 1 \
    "start of a service with a failed dependency exits non-zero"

sleep 3

# #1312: restart=true must not resurrect a service whose hard
# dependency never came up.
state=$(slinitctl status depfail-user 2>/dev/null | awk '/State:/{print $2}')
_TESTS_RUN=$((_TESTS_RUN + 1))
case "$state" in
    STARTED)
        echo "FAIL: depfail-user is STARTED despite its dependency failing"
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        ;;
    *)
        echo "OK: depfail-user stayed down (state=$state)"
        ;;
esac

# And it must be down *quietly* — no restart loop. Count how many times
# it has been started; a loop would show repeated journal lines.
starts=$(slinitctl status depfail-user 2>/dev/null | grep -c "STARTED")
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$starts" -le 1 ]; then
    echo "OK: no restart loop on the dependent service"
else
    echo "FAIL: depfail-user looks like it is restart-looping ($starts entries)"
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
fi

# PID 1 must still be healthy after all that.
out=$(slinitctl list 2>&1)
assert_contains "$out" "depfail-user" "PID 1 responsive after a dependency failure"

test_summary
