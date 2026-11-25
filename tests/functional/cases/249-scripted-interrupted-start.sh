#!/bin/sh
# 249-scripted-interrupted-start: a stop while a start-interruptible scripted
# start command runs cancels the start as dinit does — SIGINT to the start
# command, then the stop command — instead of waiting for the start to finish,
# and the cancelled start is not reported as a failure.

rm -f /run/249.stopcmd
slinitctl --system --no-wait start slowscript > /dev/null 2>&1
wait_for_service "slowscript" "STARTING" 5
assert_service_state "slowscript" "STARTING" "scripted start command running"

t0=$(date +%s)
slinitctl --system stop slowscript > /dev/null 2>&1
wait_for_service "slowscript" "STOPPED" 10
took=$(( $(date +%s) - t0 ))
assert_service_state "slowscript" "STOPPED" "stop cancelled the start (${took}s, start command sleeps 30s)"

n=0
while [ ! -s /run/249.stopcmd ] && [ "$n" -lt 30 ]; do sleep 0.1; n=$((n + 1)); done
assert_eq "$(cat /run/249.stopcmd 2>/dev/null)" "stop-command-ran" "stop command ran after the interrupted start"

slinitctl --system is-failed slowscript > /dev/null 2>&1
assert_eq "$?" "1" "a cancelled start is not a failure (is-failed false)"

assert_eq "$(ps | grep -c '[s]leep 30')" "0" "start command is gone"

test_summary
