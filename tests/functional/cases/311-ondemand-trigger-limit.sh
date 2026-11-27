#!/bin/sh
# Test: an on-demand process that never accepts its client is not
# relaunched in a tight loop.
#
# With socket-activation = on-demand, a process that exits without
# accepting leaves its client in the backlog, the socket is readable at
# once, and the process is launched again. Up to v4.0.3 nothing bounded
# that: one connect relaunched it about 2000 times in 1.5 s, flooding
# PID 1's log for as long as the client waited. Since c577455..300494f
# systemd's trigger limit applies (20 launches in 2 s): after a failed
# launch the service then fails; after a clean one the next launch is
# held back to the window's rate (about 10 a second), and the service
# stays up.

_run() { # name exit-code port
    _n="$1"; _rc="$2"; _port="$3"
    _cnt="/run/$_n.launches"
    rm -f "$_cnt"
    printf '%s\n' 'type = process' "socket-listen = tcp:127.0.0.1:$_port" \
        'socket-activation = on-demand' \
        "command = /bin/sh -c \"echo l >> $_cnt; exit $_rc\"" \
        'restart = no' > "/etc/slinit.d/$_n"
    slinitctl --system start "$_n" >/dev/null 2>&1
    wait_for_service "$_n" STARTED 10 || skip_case "$_n never listened"

    # One client that connects and then just waits.
    sleep 6 | nc 127.0.0.1 "$_port" >/dev/null 2>&1 &
    _cl=$!
    sleep 3
    _l=$(wc -l < "$_cnt" 2>/dev/null || echo 0)
    echo "$_n (exit $_rc): $_l launches in 3 s, state $(svc_state "$_n")"
    kill "$_cl" 2>/dev/null
    [ "$_l" -ge 1 ] || skip_case "$_n was never launched by the client"
    _L="$_l"
}

# Clean exit: held to the window's rate, and still up.
_run test-311-clean 0 3111
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$_L" -le 60 ]; then
    echo "OK: clean relaunches bounded ($_L in 3 s, limit 60)"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: clean relaunches unbounded ($_L in 3 s)"
fi
assert_service_state test-311-clean STARTED "clean launches do not fail the service"
slinitctl --system stop test-311-clean >/dev/null 2>&1

# Failed exit: the trigger limit fails the service after about 20.
_run test-311-fail 1 3112
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$_L" -le 25 ]; then
    echo "OK: failed relaunches stop at the trigger limit ($_L)"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: failed relaunches went past the trigger limit ($_L in 3 s)"
fi
_st=$(svc_state test-311-fail)
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$_st" != STARTED ]; then
    echo "OK: the trigger limit failed the service (state $_st)"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: still STARTED past the trigger limit"
fi

test_summary
