#!/bin/sh
# Test: slinit-getty reclaims its tty before the login prompt.
#
# Before the prompt getty now makes the tty root-owned, group tty, mode
# 0620 (0600 without a tty group), hangs it up with vhangup(2) so a
# previous session's descriptors go dead, and reopens it. vhangup also
# hangs up getty's own session, so this checks that getty survives it
# and that the tty is still its controlling terminal afterwards.

if [ ! -e /dev/tty2 ]; then
    skip_case "no /dev/tty2 in this VM"
fi

# A stale owner, as an unclean logout would leave.
chown 1000 /dev/tty2 2>/dev/null
chmod 0666 /dev/tty2

printf '%s\n' 'type = process' 'new-session = yes' 'command = /sbin/slinit-getty tty2 38400 linux' \
    'restart = false' > /etc/slinit.d/test-getty

slinitctl --system start test-getty >/dev/null 2>&1
wait_for_service test-getty STARTED 10 || true
sleep 2
assert_service_state test-getty STARTED "getty is still running after the vhangup"

_pid=$(slinitctl --system status test-getty 2>/dev/null | awk '/PID:/ { print $2; exit }')
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -n "$_pid" ] && [ -d "/proc/$_pid" ]; then
    echo "OK: getty pid $_pid alive"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: no live getty process (pid '$_pid')"
fi

_owner=$(stat -c %u /dev/tty2)
assert_eq "$_owner" "0" "the tty is root's again"
_mode=$(stat -c %a /dev/tty2)
_TESTS_RUN=$((_TESTS_RUN + 1))
case "$_mode" in
    620|600) echo "OK: tty mode $_mode" ;;
    *) _TESTS_FAILED=$((_TESTS_FAILED + 1)); echo "FAIL: tty mode $_mode, want 620 or 600" ;;
esac

# tty_nr (field 7 of /proc/PID/stat) for tty2 is 4:2 = 1026: the tty is
# still getty's controlling terminal after the hangup and reopen.
_ttynr=$(sed 's/.*) //' "/proc/$_pid/stat" 2>/dev/null | awk '{ print $5 }')
assert_eq "$_ttynr" "1026" "tty2 is getty's controlling terminal"

slinitctl --system stop test-getty >/dev/null 2>&1
test_summary
