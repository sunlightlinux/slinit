#!/bin/sh
# Test: send-sighup and slinitctl clean (4.1).
#
# test-336 ignores SIGTERM and exits on SIGHUP: with send-sighup its stop
# ends at once instead of at the 30s stop-timeout. Its state- and
# cache-directory then survive the stop, and slinitctl clean removes the
# cache by default and the state only when asked.

cat > /etc/slinit.d/test-336 <<'SVC'
type = process
command = /bin/sh -c "trap '' TERM; trap 'exit 0' HUP; while :; do sleep 0.1; done"
send-sighup = yes
stop-timeout = 30
state-directory = test-336
cache-directory = test-336
SVC

slinitctl --system start test-336 >/dev/null 2>&1
wait_for_service test-336 "STARTED" 10
sleep 1
t0=$(date +%s)
slinitctl --system stop test-336 >/dev/null 2>&1
t1=$(date +%s)
assert_service_state test-336 "STOPPED" "stopped"
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ $((t1 - t0)) -lt 10 ]; then
    echo "OK: send-sighup ended the stop in $((t1 - t0))s"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: the stop took $((t1 - t0))s: the hangup was not sent"
fi

assert_exit_code "test -d /var/lib/test-336 && test -d /var/cache/test-336" 0 "state and cache survive the stop"
out=$(slinitctl --system clean test-336 2>&1)
assert_contains "$out" "/var/cache/test-336" "the default clean names the cache"
assert_exit_code "test -d /var/cache/test-336" 1 "the cache is gone"
assert_exit_code "test -d /var/lib/test-336" 0 "the state stays without --what=state"
slinitctl --system clean --what=state test-336 >/dev/null 2>&1
assert_exit_code "test -d /var/lib/test-336" 1 "--what=state removes it"

slinitctl --system start test-336 >/dev/null 2>&1
wait_for_service test-336 "STARTED" 10
assert_exit_code "slinitctl --system clean test-336" 1 "a running service is refused"
slinitctl --system stop test-336 >/dev/null 2>&1

test_summary
