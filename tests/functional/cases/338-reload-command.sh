#!/bin/sh
# Test: reload-command (systemd's ExecReload= with a command), 4.1.
#
# slinitctl reload-signal runs the service's reload-command with
# $MAINPID set to its main process and waits for it; a failing one fails
# the reload. A systemd unit's ExecReload= command maps onto it.

pid_of() { slinitctl --system status "$1" 2>/dev/null | awk '/PID:/ { print $2; exit }'; }

cat > /etc/slinit.d/test-338 <<'SVC'
type = process
command = /bin/sleep 1000
reload-command = /bin/sh -c "echo $$MAINPID > /run/test-338.pid"
SVC
slinitctl --system start test-338 >/dev/null 2>&1
wait_for_service test-338 "STARTED" 10
assert_exit_code "slinitctl --system reload-signal test-338" 0 "the reload succeeded"
assert_eq "$(cat /run/test-338.pid 2>/dev/null)" "$(pid_of test-338)" "the command saw the main pid in \$MAINPID"
slinitctl --system stop test-338 >/dev/null 2>&1

cat > /etc/slinit.d/test-338-bad <<'SVC'
type = process
command = /bin/sleep 1000
reload-command = /bin/false
SVC
slinitctl --system start test-338-bad >/dev/null 2>&1
wait_for_service test-338-bad "STARTED" 10
assert_exit_code "slinitctl --system reload-signal test-338-bad" 1 "a failing reload-command fails the reload"
slinitctl --system stop test-338-bad >/dev/null 2>&1

slinitctl --system start test-338-unit >/dev/null 2>&1
wait_for_service test-338-unit "STARTED" 10
assert_exit_code "slinitctl --system reload-signal test-338-unit" 0 "the unit's ExecReload= ran"
assert_exit_code "test -e /run/test-338-unit.reloaded" 0 "and did what it says"
slinitctl --system stop test-338-unit >/dev/null 2>&1

# A scripted job (a oneshot, as nftables.service) reloads by its command
# while started.
cat > /etc/slinit.d/test-338-job <<'SVC'
type = scripted
command = /bin/true
reload-command = /bin/sh -c "touch /run/test-338-job.reloaded"
SVC
rm -f /run/test-338-job.reloaded
slinitctl --system start test-338-job >/dev/null 2>&1
assert_exit_code "slinitctl --system reload-signal test-338-job" 0 "a scripted job's reload-command ran"
assert_exit_code "test -e /run/test-338-job.reloaded" 0 "and did what it says"
slinitctl --system stop test-338-job >/dev/null 2>&1

test_summary
