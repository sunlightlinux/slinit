#!/bin/sh
# Test: a run-as bgprocess cannot make PID 1 adopt (and later kill) a
# root process by writing its pid into the pid file.
#
# Up to v4.0.3 a bgprocess adopted whatever pid its pid file held (only
# pid 1 and slinit itself were refused) and sent it the stop signals. A
# run-as service owns its pid file, so it could name sshd, or any other
# root process, and have PID 1 SIGTERM it on stop. Since f4c526e a pid
# from a file not owned by root is accepted only when the process runs
# as the file's owner (or is in the service's cgroup), as systemd's
# service_load_pid_file does. A service naming its own daemon still
# works.

id nobody >/dev/null 2>&1 || skip_case "no user nobody in the image"
D=/tmp/312
rm -rf "$D"; mkdir -p "$D"; chown nobody "$D"

# The victim: a root process the service has no business signalling.
setsid sleep 3120 </dev/null >/dev/null 2>&1 &
sleep 0.5
VICTIM=$(pgrep -x -f 'sleep 3120' | head -1)
[ -n "$VICTIM" ] || skip_case "victim did not start"
echo "victim pid $VICTIM (root)"

printf '%s\n' 'type = bgprocess' 'run-as = nobody' \
    "command = /bin/sh -c \"echo $VICTIM > $D/foreign.pid\"" \
    "pid-file = $D/foreign.pid" 'start-timeout = 5' 'restart = no' \
    > /etc/slinit.d/test-312-foreign
# Control: a run-as daemon naming its own background process.
printf '%s\n' 'type = bgprocess' 'run-as = nobody' \
    "command = /bin/sh -c \"sleep 3121 & echo \$\$! > $D/own.pid\"" \
    "pid-file = $D/own.pid" 'start-timeout = 5' 'restart = no' \
    > /etc/slinit.d/test-312-own

timeout 10 slinitctl --system start test-312-foreign >/dev/null 2>&1
sleep 1
_st=$(svc_state test-312-foreign)
_pid=$(timeout 5 slinitctl --system status test-312-foreign 2>/dev/null | awk '/PID:/ {print $2}')
echo "foreign: state $_st pid '$_pid'"
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$_st" != STARTED ] && [ "$_pid" != "$VICTIM" ]; then
    echo "OK: the root process named in a nobody-owned pid file was not adopted"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: adopted root process $VICTIM from a nobody-owned pid file (state $_st)"
fi
timeout 10 slinitctl --system stop test-312-foreign >/dev/null 2>&1
sleep 1
_TESTS_RUN=$((_TESTS_RUN + 1))
if kill -0 "$VICTIM" 2>/dev/null; then
    echo "OK: the root process survived the service's stop"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: stopping the service killed root process $VICTIM"
fi

timeout 10 slinitctl --system start test-312-own >/dev/null 2>&1
wait_for_service test-312-own STARTED 10
assert_service_state test-312-own STARTED "a run-as daemon naming its own process is adopted"
_own=$(cat "$D/own.pid" 2>/dev/null)
timeout 10 slinitctl --system stop test-312-own >/dev/null 2>&1
sleep 1
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -n "$_own" ] && ! kill -0 "$_own" 2>/dev/null; then
    echo "OK: and stopping it stops that process"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: own daemon '$_own' still running after stop"
fi

kill "$VICTIM" 2>/dev/null
test_summary
