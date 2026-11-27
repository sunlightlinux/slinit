#!/bin/sh
# Test: start-delay holds only its own service, and hook-timeout bounds a
# pre-start-command in both directions. Neither directive had a case.
#
# start-delay: the service waits in STARTING for the delay, but the wait
# is a timer, so another service starts and slinitctl answers meanwhile
# (slinit-service(5); before 4.0.3 the sleep-hook it replaces stalled
# every transition). Then the delayed service starts, not earlier.
#
# hook-timeout: a pre-start-command that takes 3 s fails the start when
# hook-timeout is 1 s, within a few seconds, and passes when it is 10 s.

printf '%s\n' 'type = process' 'command = /bin/sleep 1000' \
    'start-delay = 4' > /etc/slinit.d/test-331-delayed
printf '%s\n' 'type = process' 'command = /bin/sleep 1000' > /etc/slinit.d/test-331-other

_t0=$(date +%s)
slinitctl --system --no-wait start test-331-delayed >/dev/null 2>&1
sleep 0.5
assert_service_state test-331-delayed "STARTING" "start-delay: STARTING while it waits"

_ts=$(date +%s)
timeout 3 slinitctl --system start test-331-other >/dev/null 2>&1
_rc=$?
_te=$(date +%s)
assert_eq "$_rc" "0" "another service starts during the delay"
[ $((_te - _ts)) -le 2 ]
assert_eq "$?" "0" "and without waiting for it ($((_te - _ts)) s)"
assert_service_state test-331-delayed "STARTING" "the delayed one is still waiting"

_deadline=$((_t0 + 10))
while [ "$(date +%s)" -lt "$_deadline" ] && [ "$(svc_state test-331-delayed)" != "STARTED" ]; do
    sleep 0.2
done
_el=$(( $(date +%s) - _t0 ))
assert_service_state test-331-delayed "STARTED" "the delayed service starts once the delay is over"
[ "$_el" -ge 3 ]
assert_eq "$?" "0" "not before the delay ($_el s after the start)"

for v in 1:fail 10:ok; do
    _to=${v%%:*} _want=${v##*:}
    printf '%s\n' 'type = process' 'command = /bin/sleep 1000' \
        'pre-start-command = /bin/sleep 3' "hook-timeout = $_to" > /etc/slinit.d/test-331-hook-$_to
    _ts=$(date +%s)
    timeout 20 slinitctl --system start test-331-hook-$_to >/dev/null 2>&1
    _rc=$?
    _el=$(( $(date +%s) - _ts ))
    if [ "$_want" = fail ]; then
        [ "$_rc" != "0" ] && [ "$(svc_state test-331-hook-$_to)" != "STARTED" ]
        assert_eq "$?" "0" "hook-timeout = 1: a 3 s pre-start-command fails the start (rc $_rc)"
        [ "$_el" -le 2 ]
        assert_eq "$?" "0" "hook-timeout = 1: it fails without waiting for the hook ($_el s)"
    else
        assert_eq "$_rc" "0" "hook-timeout = 10: the 3 s pre-start-command is let finish"
        assert_service_state test-331-hook-$_to "STARTED" "hook-timeout = 10: the service starts"
    fi
done

slinitctl --system stop test-331-delayed >/dev/null 2>&1
slinitctl --system stop test-331-other >/dev/null 2>&1
slinitctl --system stop test-331-hook-10 >/dev/null 2>&1
test_summary
