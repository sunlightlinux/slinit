#!/bin/sh
# 242-stop-command-completion — a service must not report STOPPED while
# its stop-command is still running.
#
# The main process exiting is NOT the same event as the stop having
# finished. A stop-command is commonly written to kill the daemon first
# and then clean up after it, so when slinit sees the main process go,
# the cleanup is still in flight. Reporting STOPPED there lets
# dependents and shutdown proceed as though it had completed.
#
# Measured before the fix: STOPPED for the whole five seconds the
# cleanup was still running. Same shape as dinit 4da302f / 798e037,
# which guard on stop_pid for the same reason, and the other half of
# the orphaned-daemon problem `--fast` caused in v2.3.9.
#
# The second service covers the risk the fix introduces: if the
# transition now waits for the stop-command, a wedged stop-command must
# not hold the service in STOPPING forever. The stop-timeout has to end
# it.

# --- 1. the stop must wait for a slow stop-command ---
slinitctl --system start slowstop > /dev/null 2>&1
_s=$(slinitctl --system status slowstop 2>/dev/null | awk '/^ *State:/ { print $2; exit }')
assert_eq "$_s" "STARTED" "the service with a slow stop-command starts"

rm -f /run/cleanup-done
slinitctl --system stop slowstop > /dev/null 2>&1 &
sleep 2

_mid=$(slinitctl --system status slowstop 2>/dev/null | awk '/^ *State:/ { print $2; exit }')
_done_mid=$([ -f /run/cleanup-done ] && echo yes || echo no)
echo "INFO: 2s into the stop: state=$_mid cleanup-done=$_done_mid"

# The assertion that matters. Both halves are needed: STOPPING alone
# could mean the stop never started, so the cleanup marker must still
# be absent too.
assert_eq "$_done_mid" "no" "the cleanup has genuinely not finished yet"
assert_eq "$_mid" "STOPPING" "the service is still STOPPING while its stop-command runs"

_w=0
while [ "$_w" -lt 40 ]; do
    [ -f /run/cleanup-done ] && break
    sleep 0.5
    _w=$((_w + 1))
done
sleep 1
_end=$(slinitctl --system status slowstop 2>/dev/null | awk '/^ *State:/ { print $2; exit }')
_done_end=$([ -f /run/cleanup-done ] && echo yes || echo no)
echo "INFO: after the cleanup: state=$_end cleanup-done=$_done_end"
assert_eq "$_done_end" "yes" "the cleanup ran to completion"
assert_eq "$_end" "STOPPED" "the service reaches STOPPED once the stop-command finishes"

# --- 2. a wedged stop-command must still time out ---
slinitctl --system start wedgedstop > /dev/null 2>&1
_s2=$(slinitctl --system status wedgedstop 2>/dev/null | awk '/^ *State:/ { print $2; exit }')
assert_eq "$_s2" "STARTED" "the service with a wedged stop-command starts"

slinitctl --system stop wedgedstop > /dev/null 2>&1 &
_w=0
while [ "$_w" -lt 40 ]; do
    _st=$(slinitctl --system status wedgedstop 2>/dev/null | awk '/^ *State:/ { print $2; exit }')
    [ "$_st" = "STOPPED" ] && break
    sleep 0.5
    _w=$((_w + 1))
done
echo "INFO: wedged stop ended as $_st after ~$((_w / 2))s (stop-timeout = 4)"
assert_eq "$_st" "STOPPED" "the stop-timeout still ends a wedged stop-command"

test_summary
