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

# depfail-user appends a line to this file every time it is started.
STARTS=/run/220.starts
rm -f "$STARTS"

# depfail-base runs /bin/false, so it can never come up.
slinitctl start depfail-base >/dev/null 2>&1
sleep 2

# Printed on every run. On the CI runner depfail-user reaches STARTED and
# the start exits 0, which can only happen if the hard dependency was not
# failed at this point — so the dependency's own state is the fact worth
# capturing, and it is the one the assertions below do not record. Passes
# here under every condition tried, so a baseline from a green run is what
# makes the red one readable.
echo "--- diag: depfail-base (the dependency that must have failed) ---"
slinitctl status depfail-base 2>&1 || true
echo "--- diag: end ---"

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

# And it must be down *quietly* — no restart loop, and no start at all.
# Its hard dependency never came up, so the process must never have run:
# the expected count is exactly 0. One status snapshot cannot show this
# (it holds one State: line whatever the history), so count the lines
# the service itself leaves behind. Two more seconds give a loop that
# slipped past the checks above the time to show up.
sleep 2
if [ -f "$STARTS" ]; then
    starts=$(grep -c started "$STARTS")
else
    starts=0
fi
assert_eq "$starts" "0" "depfail-user was never started (starts recorded in $STARTS)"

# PID 1 must still be healthy after all that.
out=$(slinitctl list 2>&1)
assert_contains "$out" "depfail-user" "PID 1 responsive after a dependency failure"

test_summary
