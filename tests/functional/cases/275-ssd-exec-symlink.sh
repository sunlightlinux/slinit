#!/bin/sh
# Test: slinit-start-stop-daemon --exec across symlinks.
#
# 1. Merged /usr: a daemon started as /tmp/275/sbin/sleep, where sbin is
#    a symlink to bin, runs as /tmp/275/bin/sleep per /proc/PID/exe. The
#    --exec spelling through the link must still find it: a second
#    --start reports it running, --stop stops it.
# 2. A busybox applet: --exec naming a symlink to /bin/busybox must NOT
#    match every busybox process (ash, getty, this very shell). Matched
#    by inode, it did: --start refused with "already running" and --stop
#    signalled them all.

T=/tmp/275
rm -rf $T
mkdir -p $T/bin
# A copy, not a link: its own inode, and busybox still runs as `sleep`.
cp /bin/busybox $T/bin/sleep
ln -s bin $T/sbin
ln -s /bin/busybox $T/bin/crond
SSD=slinit-start-stop-daemon

$SSD --start --background --exec $T/sbin/sleep -- 313.9 >/tmp/275.out 2>&1
assert_eq "$?" "0" "first --start through the symlinked dir"
sleep 0.5
_pid=$(ps -o pid,args | awk '/[s]leep 313.9/ { print $1; exit }')
assert_eq "$(readlink /proc/$_pid/exe 2>/dev/null)" "$T/bin/sleep" \
    "the kernel reports the resolved path for the daemon"

$SSD --start --background --exec $T/sbin/sleep -- 313.9 >/tmp/275.out 2>&1
assert_eq "$?" "1" "second --start reports it already running"
assert_eq "$(ps -o args | grep -c '[s]leep 313.9')" "1" "no second copy was spawned"

$SSD --status --exec $T/sbin/sleep >/dev/null 2>&1
assert_eq "$?" "0" "--status finds it through the symlinked dir"

# The busybox applet link: nothing runs as crond, although this shell,
# getty and friends all run /bin/busybox.
$SSD --status --exec $T/bin/crond >/tmp/275.status 2>&1
_rc=$?
echo "applet --status rc=$_rc matched: $(tr '\n' ' ' < /tmp/275.status)"
assert_eq "$_rc" "3" "--exec on a busybox applet link matches no other busybox process"

$SSD --stop --exec $T/sbin/sleep --retry TERM/3 >/tmp/275.out 2>&1
assert_eq "$?" "0" "--stop through the symlinked dir"
sleep 0.3
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -n "$_pid" ] && [ -d /proc/$_pid ] && \
   [ "$(awk '/^State/ { print $2 }' /proc/$_pid/status)" != "Z" ]; then
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: pid $_pid survived --stop"
    kill "$_pid" 2>/dev/null
else
    echo "OK: the daemon is gone after --stop"
fi

rm -rf $T
test_summary
