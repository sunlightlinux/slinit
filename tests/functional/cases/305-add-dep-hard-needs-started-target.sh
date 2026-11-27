#!/bin/sh
# Test: `add-dep A depends-on B` is refused while A runs and B is down.
#
# Up to v4.0.3 the edge went in and B was asked to start. A B that failed
# left A STARTED on a STOPPED hard dependency (still counted as required
# by it), which the state machine never allows otherwise. dinit refuses
# the edge with NAK, and since b11efe9 so does slinit. A soft dependency
# (waits-for) to the same stopped service is still accepted, and so is
# the hard edge once B is started.

printf '%s\n' 'type = process' 'command = /bin/sleep 3051' 'restart = false' \
    > /etc/slinit.d/test-305-a
# B fails every start: its command exits at once with a failure.
printf '%s\n' 'type = scripted' 'command = /bin/false' 'restart = false' \
    > /etc/slinit.d/test-305-b

slinitctl --system start test-305-a >/dev/null 2>&1
wait_for_service test-305-a STARTED 10
slinitctl --system status test-305-b >/dev/null 2>&1   # load, do not start
[ "$(svc_state test-305-b)" = STOPPED ] || skip_case "test-305-b not STOPPED before add-dep"

_out=$(timeout 10 slinitctl --system add-dep test-305-a depends-on test-305-b 2>&1)
_rc=$?
echo "hard add-dep: rc=$_rc out=$_out"
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$_rc" -ne 0 ]; then
    echo "OK: hard add-dep to a stopped service from a running one is refused"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: hard add-dep was accepted (rc=0)"
fi

sleep 1
assert_service_state test-305-a STARTED "the running service is left running"
assert_service_state test-305-b STOPPED "the refused edge did not start its target"
# No edge was installed: removing it must fail too.
timeout 5 slinitctl --system rm-dep test-305-a depends-on test-305-b >/dev/null 2>&1
_rc=$?
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$_rc" -ne 0 ]; then
    echo "OK: no depends-on edge was left behind"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: rm-dep found a depends-on edge the refused add-dep left behind"
fi

# A soft dependency to the same stopped service is unchanged.
_out=$(timeout 10 slinitctl --system add-dep test-305-a waits-for test-305-b 2>&1)
assert_contains "$_out" "Added" "a waits-for edge to a stopped service is still accepted"
timeout 5 slinitctl --system rm-dep test-305-a waits-for test-305-b >/dev/null 2>&1

# And the hard edge once its target is up.
printf '%s\n' 'type = process' 'command = /bin/sleep 3052' 'restart = false' \
    > /etc/slinit.d/test-305-c
slinitctl --system start test-305-c >/dev/null 2>&1
wait_for_service test-305-c STARTED 10
_out=$(timeout 10 slinitctl --system add-dep test-305-a depends-on test-305-c 2>&1)
assert_contains "$_out" "Added" "a hard edge to a started service is still accepted"

test_summary
