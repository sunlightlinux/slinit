#!/bin/sh
# 251-stop-command-run-as: the stop-command of a run-as service runs as that
# user (systemd parity), not as root — it used to run with PID 1's full
# privileges for an unprivileged service.

mkdir -p /run/251 && chmod 0777 /run/251 && rm -f /run/251/stop.uid
want=$(id -u nobody)
slinitctl --system start runasstop > /dev/null 2>&1
wait_for_service "runasstop" "STARTED" 5
slinitctl --system stop runasstop > /dev/null 2>&1
wait_for_service "runasstop" "STOPPED" 10
n=0
while [ ! -s /run/251/stop.uid ] && [ "$n" -lt 30 ]; do sleep 0.1; n=$((n + 1)); done
assert_eq "$(cat /run/251/stop.uid 2>/dev/null)" "$want" "stop-command ran as run-as user (uid $want)"

test_summary
