#!/bin/sh
# 20-bgprocess-pidfile — type=bgprocess. The launched command double-forks
# a grandchild and writes its pid to pid-file, then exits 0. slinit must
# adopt the pid-file PID as the tracked service PID and treat the service
# as STARTED for as long as that PID lives.

SVC="acceptance-test-bgproc"
PIDFILE="/run/${SVC}.pid"

cleanup() {
    svc_remove "$SVC"
    # If anything leaked, kill by pidfile contents.
    if [ -r "$PIDFILE" ]; then
        _stale=$(cat "$PIDFILE" 2>/dev/null)
        [ -n "$_stale" ] && kill -KILL "$_stale" 2>/dev/null || true
    fi
    rm -f "$PIDFILE" "/tmp/${SVC}-daemon.sh"
}
trap cleanup EXIT INT TERM

# The launcher sh forks a grandchild via `setsid` (new session — survives
# the launcher's exit without picking up SIGHUP from group teardown), then
# the grandchild writes its own PID and exec's into sleep so the same PID
# stays alive. The launcher exits 0; slinit's bgprocess type reads pid-file
# and adopts the grandchild. (Without setsid + exec the inner sh has been
# observed to die between the launcher's exit and slinit's first probe.)
DAEMON="/tmp/${SVC}-daemon.sh"
cat > "$DAEMON" <<EOF
#!/bin/sh
echo \$\$ > $PIDFILE
exec sleep 3600
EOF
chmod +x "$DAEMON"
svc_deploy "$SVC" <<EOF
type = bgprocess
pid-file = $PIDFILE
command = /bin/sh -c 'setsid /bin/sh $DAEMON & sleep 1; exit 0'
restart = false
EOF

slinitctl --system start "$SVC" >/dev/null 2>&1
wait_for_service "$SVC" "STARTED" 10 || true
assert_service_state "$SVC" "STARTED" "$SVC reached STARTED"

# The pidfile must exist and point at a live process.
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -r "$PIDFILE" ]; then
    echo "OK: pid-file $PIDFILE created"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: pid-file $PIDFILE missing"
    test_summary
    exit 1
fi

_pid_in_file=$(cat "$PIDFILE")
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -d "/proc/$_pid_in_file" ]; then
    echo "OK: tracked grandchild $_pid_in_file is alive"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: tracked pid $_pid_in_file not in /proc"
fi

# slinit must report the *same* pid as the pid-file (it adopted it).
_pid_in_slinit=$(slinitctl --system status "$SVC" 2>/dev/null | awk '/PID:/ {print $2; exit}')
assert_eq "$_pid_in_slinit" "$_pid_in_file" "slinit PID matches pid-file PID"

# Stopping the service must reap the grandchild.
slinitctl --system stop "$SVC" >/dev/null 2>&1
wait_for_service "$SVC" "STOPPED" 10 || true
assert_service_state "$SVC" "STOPPED" "$SVC reached STOPPED"

# Give the kernel a tick to clean up.
sleep 1
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ ! -d "/proc/$_pid_in_file" ]; then
    echo "OK: grandchild $_pid_in_file reaped on stop"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: pid $_pid_in_file still alive after stop"
fi

test_summary
