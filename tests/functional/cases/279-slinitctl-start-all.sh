#!/bin/sh
# Test: `slinitctl start-all` starts every loaded STOPPED service, and
# passes over the three kinds it documents as off limits: manual = yes,
# refuse-manual-start = yes, and a service pinned stopped. A bulk start is
# not the explicit `slinitctl start NAME` those settings reserve.
#
# start-all had no functional case. The skip list is the part that
# matters: one missing check and start-all quietly starts something the
# operator said must not start on its own.

for n in 1 2; do
    printf '%s\n' 'type = process' "command = /bin/sleep 2791.$n" \
        'restart = false' > "/etc/slinit.d/test-sa-plain$n"
done
printf '%s\n' 'type = process' 'command = /bin/sleep 2792' 'manual = yes' \
    'restart = false' > /etc/slinit.d/test-sa-manual
printf '%s\n' 'type = process' 'command = /bin/sleep 2793' \
    'refuse-manual-start = yes' 'restart = false' > /etc/slinit.d/test-sa-refuse
printf '%s\n' 'type = process' 'command = /bin/sleep 2794' \
    'restart = false' > /etc/slinit.d/test-sa-pinned

# start-all walks loaded services; status loads without starting.
for n in plain1 plain2 manual refuse pinned; do
    slinitctl --system status "test-sa-$n" >/dev/null 2>&1
done
slinitctl --system --pin stop test-sa-pinned >/dev/null 2>&1

_out=$(timeout 10 slinitctl --system start-all 2>&1)
assert_eq "$?" "0" "start-all succeeds"
echo "start-all: $_out"
# At least the two plain services; the base image may have more stopped.
_n=$(echo "$_out" | sed -n 's/^Starting \([0-9]*\) service.*/\1/p')
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -n "$_n" ] && [ "$_n" -ge 2 ]; then
    echo "OK: start-all reports $_n started"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: start-all reported '$_n' started, expected at least 2"
fi

wait_for_service test-sa-plain1 STARTED 10 || true
wait_for_service test-sa-plain2 STARTED 10 || true
assert_service_state test-sa-plain1 STARTED "a stopped service is started"
assert_service_state test-sa-plain2 STARTED "every stopped service is started"
sleep 1
assert_service_state test-sa-manual STOPPED "manual = yes is left alone"
assert_service_state test-sa-refuse STOPPED "refuse-manual-start is left alone"
assert_service_state test-sa-pinned STOPPED "a service pinned stopped is left alone"

# Second run: nothing stopped is eligible any more, so plain1/plain2
# count as skipped now and nothing new is started from our set.
_out2=$(timeout 10 slinitctl --system start-all 2>&1)
_skip=$(echo "$_out2" | sed -n 's/.*; \([0-9]*\) already.*/\1/p')
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -n "$_skip" ] && [ "$_skip" -ge 5 ]; then
    echo "OK: second start-all skips $_skip"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: second start-all skipped '$_skip', expected at least 5 ($_out2)"
fi
assert_service_state test-sa-manual STOPPED "still left alone on a second run"

slinitctl --system unpin test-sa-pinned >/dev/null 2>&1
for n in plain1 plain2; do slinitctl --system stop "test-sa-$n" >/dev/null 2>&1; done
test_summary
