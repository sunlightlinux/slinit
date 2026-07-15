#!/bin/sh
# Test: options = no-new-privs sets PR_SET_NO_NEW_PRIVS on the child,
# observable as NoNewPrivs: 1 in /proc/PID/status. A control service
# without the option must show NoNewPrivs: 0 so the test proves the
# directive actually flips the bit rather than passing on a default.

_read_pid() {
    _svc="$1"
    _p=""
    _i=0
    while [ "$_i" -lt 5 ]; do
        _p=$(slinitctl --system status "$_svc" 2>/dev/null | awk '/PID:/ { print $2; exit }')
        [ -n "$_p" ] && [ "$_p" != "0" ] && break
        sleep 0.2
        _i=$((_i + 1))
    done
    echo "$_p"
}

wait_for_service "nnp-svc" "STARTED" 10
wait_for_service "nnp-off-svc" "STARTED" 10

_pid_on=$(_read_pid nnp-svc)
_pid_off=$(_read_pid nnp-off-svc)

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -z "$_pid_on" ] || [ -z "$_pid_off" ]; then
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: could not resolve PIDs (on=$_pid_on off=$_pid_off)"
    test_summary
    return
fi
echo "OK: nnp-svc pid=$_pid_on nnp-off-svc pid=$_pid_off"

_nnp_on=$(awk '/^NoNewPrivs:/ { print $2; exit }' "/proc/$_pid_on/status" 2>/dev/null)
_nnp_off=$(awk '/^NoNewPrivs:/ { print $2; exit }' "/proc/$_pid_off/status" 2>/dev/null)

assert_eq "$_nnp_on" "1" "NoNewPrivs=1 when options=no-new-privs"
assert_eq "$_nnp_off" "0" "NoNewPrivs=0 by default (control service)"

test_summary
