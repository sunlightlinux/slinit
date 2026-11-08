#!/bin/sh
# 245-stop-restart-wait — stop and restart wait for the outcome, as start
# has since v2.3.5.
#
# Both used to return as soon as the daemon had accepted the request:
# `stop` said "stopped" with the service still STOPPING, and `restart`
# said "restarted", exit 0, when its start half had failed, so
# `slinitctl restart foo && ...` went on regardless. --no-wait keeps that.

cat > /run/slowgo.sh <<'SCRIPT'
trap 'sleep 1; exit 0' TERM
while :; do sleep 0.1 & wait $!; done
SCRIPT

slinitctl --system start slowgo > /dev/null 2>&1
wait_for_service "slowgo" "STARTED" 10
pid1=$(slinitctl --system status slowgo 2>/dev/null | awk '/PID:/ { print $2; exit }')
sleep 0.5 # let the script reach its loop and set its trap

# --- restart returns with the service back up, a new process in it ---
assert_exit_code "slinitctl --system restart slowgo" 0 "restart succeeds"
assert_eq "$(svc_state slowgo)" "STARTED" "restart returns with the service STARTED"
pid2=$(slinitctl --system status slowgo 2>/dev/null | awk '/PID:/ { print $2; exit }')
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -n "$pid2" ] && [ "$pid1" != "$pid2" ]; then
    echo "OK: a new process ($pid1 -> $pid2)"
else
    echo "FAIL: PID did not change ($pid1 -> $pid2)"
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
fi
sleep 0.5

# --- stop returns with the service stopped ---
assert_exit_code "slinitctl --system stop slowgo" 0 "stop succeeds"
assert_eq "$(svc_state slowgo)" "STOPPED" "stop returns with the service STOPPED"

# --- --no-wait returns while it is still stopping ---
slinitctl --system start slowgo > /dev/null 2>&1
sleep 0.5
assert_exit_code "slinitctl --system --no-wait stop slowgo" 0 "stop --no-wait succeeds"
assert_eq "$(svc_state slowgo)" "STOPPING" "stop --no-wait returns while it is STOPPING"
wait_for_service "slowgo" "STOPPED" 10

# --- a restart whose start fails says so ---
touch /run/gate
slinitctl --system start gated > /dev/null 2>&1
assert_eq "$(svc_state gated)" "STARTED" "gated starts while its gate is open"
rm -f /run/gate
assert_exit_code "slinitctl --system restart gated" 1 "restart fails when its start half fails"
assert_eq "$(svc_state gated)" "STOPPED" "gated is STOPPED after the failed restart"
assert_exit_code "slinitctl --system --no-wait restart gated" 0 "restart --no-wait does not wait for that"

test_summary
