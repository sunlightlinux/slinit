#!/bin/sh
# 246-start-when-idle — systemd's Type=idle: a service waits for the others
# to finish starting, at most five seconds. It keeps a login prompt from
# being printed over by a service still coming up.

# --- nothing else is starting: no wait ---
t0=$(date +%s%N)
slinitctl --system start idler > /dev/null 2>&1
elapsed=$(( ($(date +%s%N) - t0) / 1000000 ))
assert_eq "$(svc_state idler)" "STARTED" "idler starts with nothing else starting"
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$elapsed" -lt 1000 ]; then
    echo "OK: no wait with nothing to wait for (${elapsed} ms)"
else
    echo "FAIL: took ${elapsed} ms with nothing to wait for"
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
fi
slinitctl --system stop idler > /dev/null 2>&1

# --- another service is starting: waits for it ---
slinitctl --system --no-wait start busy > /dev/null 2>&1
slinitctl --system --no-wait start idler > /dev/null 2>&1
sleep 1
assert_eq "$(svc_state busy)" "STARTING" "busy is still starting"
assert_eq "$(svc_state idler)" "STARTING" "idler waits while busy starts"
wait_for_service "idler" "STARTED" 5
assert_eq "$(svc_state busy)" "STARTED" "idler started once busy had finished"
slinitctl --system stop idler > /dev/null 2>&1
slinitctl --system stop busy > /dev/null 2>&1

# --- one that never finishes holds it five seconds, no more ---
slinitctl --system --no-wait start wedged > /dev/null 2>&1
t0=$(date +%s)
slinitctl --system start idler > /dev/null 2>&1
elapsed=$(( $(date +%s) - t0 ))
assert_eq "$(svc_state wedged)" "STARTING" "wedged has not finished"
assert_eq "$(svc_state idler)" "STARTED" "idler started anyway"
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$elapsed" -ge 4 ] && [ "$elapsed" -le 7 ]; then
    echo "OK: the wait ended at its bound (${elapsed} s)"
else
    echo "FAIL: waited ${elapsed} s, want about 5"
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
fi
slinitctl --system stop idler > /dev/null 2>&1
slinitctl --system --no-wait stop wedged > /dev/null 2>&1

test_summary
