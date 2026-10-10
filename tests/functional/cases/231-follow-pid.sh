#!/bin/sh
# 231-follow-pid — `follow-pid` adopts a process slinit did not start.
#
# The stranger is launched by this script and writes its own pid, the way
# a daemon's owner would. slinit is not its parent, so wait4(2) is not
# available: the feature rests on the same poll that supervises a launched
# bgprocess, which also notices a zombie and compares /proc start time so
# a recycled pid is not taken for the original.

PIDFILE=/run/follow-stranger.pid

# The shell writes its pid and then execs, so the file names the live
# process rather than a wrapper that has already gone.
#
# The redirection is load-bearing, not tidiness. guest-runner.sh collects
# a case's output with `_output=$( { . case; } 2>&1 | tee /dev/console )`,
# and a background process started by the case inherits that pipe. The
# command substitution does not return until the pipe closes, so a
# stranger holding it open parks the whole case for its entire lifetime —
# 600 seconds here — and the harness reports "no result received" with an
# empty console log, before even the first echo. Detaching its stdio is
# also what a real daemon's owner does.
start_stranger() {
    /bin/sh -c "echo \$\$ > $PIDFILE; exec sleep 600" >/dev/null 2>&1 &
    _tries=0
    while [ "$_tries" -lt 20 ]; do
        [ -s "$PIDFILE" ] && break
        sleep 0.2
        _tries=$((_tries + 1))
    done
    cat "$PIDFILE" 2>/dev/null
}

rm -f "$PIDFILE"
_fp_pid=$(start_stranger)
echo "INFO: stranger pid=$_fp_pid"

cat > /etc/slinit.d/test-follow <<EOF
type = bgprocess
follow-pid = $PIDFILE
restart = no
EOF

slinitctl --system start test-follow 2>/dev/null
wait_for_service test-follow STARTED 15
assert_service_state test-follow "STARTED" "follow-pid service with no command reached STARTED"

_fp_got=$(slinitctl --system status test-follow 2>/dev/null | awk '/PID:/ { print $2; exit }')
# Case-local names are prefixed: the shared helpers have no `local`, and
# wait_for_service assigns _want/_got itself. Calling it between setting
# _want and reading it silently replaced the pid with "STARTED", so the
# assertion compared a pid against a state and the diff read as a feature
# bug. The perf harness prefixes for the same reason.
assert_eq "$_fp_got" "$_fp_pid" "slinit adopted the pid from the file, not one of its own"
assert_eq "$(cat "/proc/$_fp_got/comm" 2>/dev/null)" "sleep" \
    "the adopted pid is the stranger itself, not slinit-runner"

# Stopping an adopted process signals it. /proc vanishing is the right
# check: the stranger is not slinit's child, so no reaper of ours can
# leave it a zombie that kill -0 would still call alive.
slinitctl --system stop test-follow 2>/dev/null
wait_for_service test-follow STOPPED 15
_TESTS_RUN=$((_TESTS_RUN + 1))
_e=0
while [ "$_e" -lt 8 ]; do
    [ -d "/proc/$_fp_pid" ] || break
    sleep 1
    _e=$((_e + 1))
done
if [ -d "/proc/$_fp_pid" ]; then
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: adopted process $_fp_pid survived the service stop"
else
    echo "OK: stopping the service signalled the adopted process"
fi

# A pid file that is not there yet is waited for, not failed: slinit is
# routinely asked to follow something started later in the boot. A
# launched bgprocess treats a missing file as fatal; following must not.
rm -f "$PIDFILE"
cat > /etc/slinit.d/test-follow-late <<EOF
type = bgprocess
follow-pid = $PIDFILE
start-timeout = 25
restart = no
EOF

slinitctl --system --no-wait start test-follow-late 2>/dev/null
sleep 2
_state=$(slinitctl --system status test-follow-late 2>/dev/null | awk '/^ *State:/ { print $2; exit }')
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$_state" = "STOPPED" ]; then
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: a missing pid file failed the start immediately"
else
    echo "OK: a missing pid file is waited for, not failed (state=$_state)"
fi

_fp_late=$(start_stranger)
echo "INFO: late stranger pid=$_fp_late"
wait_for_service test-follow-late STARTED 20
assert_service_state test-follow-late "STARTED" "the pid file appearing late completed the start"

slinitctl --system stop test-follow-late 2>/dev/null
kill -9 "$_fp_late" 2>/dev/null
rm -f "$PIDFILE" /etc/slinit.d/test-follow /etc/slinit.d/test-follow-late

test_summary
