#!/bin/sh
# Test: odd entries in the service directory never block or kill PID 1.
#
# The .d/rootfs/sbin/init wrapper plants, before slinit starts, five
# "service files": a FIFO, a dangling symlink, a directory, a 10 MiB
# file with no newline, and 10 MiB of comment lines ending in a valid
# description. Each is then started by name. Opening the FIFO for
# reading would block with nothing at the other end, and a load runs
# with the loader lock held, so a blocking open there would stall every
# later start and reload. Each broken one must fail with an error that
# says what is wrong, quickly; the comment file is a valid description
# and must load. (A boot that depends on any of them, even through
# waits-for, fails to load as a whole, as dinit's does: that path is
# the recovery menu, covered by 287.)

CATCH_ALL=/run/slinit/catch-all.log
_state() { slinitctl --system status "$1" 2>/dev/null | awk '/State:/ {print $2; exit}'; }

wait_for_service boot STARTED 10
assert_eq "$(_state boot)" "STARTED" "boot reached STARTED with the odd entries present"

# _start_fails NAME WHY — the start must fail, within 15s, and the
# client's error must contain WHY.
_start_fails() {
    _t0=$(cut -d. -f1 /proc/uptime)
    _out=$(timeout 15 slinitctl --system start "$1" 2>&1); _rc=$?
    _dt=$(( $(cut -d. -f1 /proc/uptime) - _t0 ))
    echo "start $1: rc=$_rc in ${_dt}s: $(echo "$_out" | cut -c1-240)"
    _TESTS_RUN=$((_TESTS_RUN + 1))
    if [ "$_rc" -ne 0 ] && [ "$_rc" -ne 124 ] && [ "$_dt" -le 5 ]; then
        echo "OK: $1: the start fails, in ${_dt}s"
    else
        echo "FAIL: $1: rc=$_rc after ${_dt}s (want a failure within 5s)"
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
    fi
}

_start_fails fifo-svc
_start_fails dangle-svc
_start_fails dir-svc
_start_fails bigline-svc

# The client gets an opcode only; the reason has to be in slinit's log.
sleep 1
_log=$(cat "$CATCH_ALL" 2>/dev/null)
echo "--- catch-all:"; echo "$_log" | grep -a -- '-svc' | cut -c1-300; echo "---"
for _s in fifo-svc dir-svc; do
    _l=$(echo "$_log" | grep -a "Could not load service '$_s'")
    assert_contains "$_l" "not a regular file" "$_s: the log says it is not a regular file"
done
_l=$(echo "$_log" | grep -a "Could not load service 'bigline-svc'")
assert_contains "$_l" "/etc/slinit.d/bigline-svc" "bigline-svc: the log names the file"
_out=$(timeout 15 slinitctl --system start bigcomment-svc 2>&1); _rc=$?
assert_eq "$_rc" "0" "the 10 MiB comment file loads and starts"
assert_eq "$(_state bigcomment-svc)" "STARTED" "bigcomment-svc is STARTED"

_out=$(timeout 5 slinitctl --system list 2>&1); _rc=$?
assert_eq "$_rc" "0" "slinitctl list answers afterwards"
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$(cat /proc/1/comm)" = "slinit" ] && timeout 5 slinitctl --system list >/dev/null 2>&1; then
    echo "OK: PID 1 alive and answering"
else
    echo "FAIL: PID 1 not answering"; _TESTS_FAILED=$((_TESTS_FAILED + 1))
fi
echo "slinit VmRSS: $(awk '/VmRSS/ {print $2, $3}' /proc/1/status)"

test_summary
