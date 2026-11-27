#!/bin/sh
# Test: part-of (systemd's PartOf=) under PID 1.
#
# test-334-m is part-of test-334-x and does not depend on it. A restart
# of x restarts m (a new pid), a stop of x stops m, a stop of m leaves x
# alone. The same for a systemd unit with PartOf= loaded directly
# (334-part-of.d/systemd).

pid_of() { slinitctl --system status "$1" 2>/dev/null | awk '/PID:/ { print $2; exit }'; }

cat > /etc/slinit.d/test-334-x <<'SVC'
type = process
command = /bin/sleep 1000
SVC
cat > /etc/slinit.d/test-334-m <<'SVC'
type = process
command = /bin/sleep 1000
part-of: test-334-x
SVC

slinitctl --system start test-334-x >/dev/null 2>&1
slinitctl --system start test-334-m >/dev/null 2>&1
assert_service_state test-334-m "STARTED" "member started on its own"
m1=$(pid_of test-334-m)

slinitctl --system restart test-334-x >/dev/null 2>&1
wait_for_service test-334-m "STARTED" 10
m2=$(pid_of test-334-m)
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -n "$m1" ] && [ -n "$m2" ] && [ "$m1" != "$m2" ]; then
    echo "OK: a restart of x restarted m ($m1 -> $m2)"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: a restart of x did not restart m (pid $m1 -> $m2)"
fi

slinitctl --system stop test-334-m >/dev/null 2>&1
assert_service_state test-334-x "STARTED" "a stop of m leaves x running"
slinitctl --system start test-334-m >/dev/null 2>&1
slinitctl --system stop test-334-x >/dev/null 2>&1
wait_for_service test-334-m "STOPPED" 10
assert_service_state test-334-m "STOPPED" "a stop of x stopped m"

# A unit's PartOf=.
slinitctl --system start test-334-x >/dev/null 2>&1
slinitctl --system start test-334-unit >/dev/null 2>&1
assert_service_state test-334-unit "STARTED" "PartOf= unit started on its own"
slinitctl --system stop test-334-x >/dev/null 2>&1
wait_for_service test-334-unit "STOPPED" 10
assert_service_state test-334-unit "STOPPED" "a stop of x stopped the PartOf= unit"

test_summary
