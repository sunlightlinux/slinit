#!/bin/sh
# 253-waits-for-restart-after-dep-stop: a waits-for dependency never blocks.
# After the operator stopped the soft dependency, an auto-restart of the
# dependent used to wait for it forever (STARTING, no process).

slinitctl --system start crashy > /dev/null 2>&1
wait_for_service "crashy" "STARTED" 5
wait_for_service "softdep" "STARTED" 5

slinitctl --system stop softdep > /dev/null 2>&1
wait_for_service "softdep" "STOPPED" 5
assert_service_state "crashy" "STARTED" "dependent keeps running when its waits-for dependency stops"

pid=$(timeout 5 slinitctl --system status crashy | awk '/PID:/ {print $2; exit}')
kill -9 "$pid" 2>/dev/null
sleep 0.5
wait_for_service "crashy" "STARTED" 5
newpid=$(timeout 5 slinitctl --system status crashy | awk '/PID:/ {print $2; exit}')
assert_service_state "crashy" "STARTED" "dependent restarted after its crash"
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -n "$newpid" ] && [ "$newpid" != "0" ] && [ "$newpid" != "$pid" ]; then
    echo "OK: new process ($pid -> $newpid)"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: no new process after restart (old $pid, now '$newpid')"
fi
assert_service_state "softdep" "STOPPED" "the stopped soft dependency is not pulled back up"

slinitctl --system stop crashy > /dev/null 2>&1
test_summary
