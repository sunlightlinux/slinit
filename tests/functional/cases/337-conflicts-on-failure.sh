#!/bin/sh
# Test: conflicts and on-failure under PID 1 (4.1).
#
# test-337-a conflicts with test-337-b: starting either stops the other.
# test-337-f exits 1 with no restart: its on-failure service
# test-337-h starts; test-337-ok exits 0 and starts nothing.

cat > /etc/slinit.d/test-337-a <<'SVC'
type = process
command = /bin/sleep 1000
conflicts: test-337-b
SVC
cat > /etc/slinit.d/test-337-b <<'SVC'
type = process
command = /bin/sleep 1000
SVC
cat > /etc/slinit.d/test-337-h <<'SVC'
type = process
command = /bin/sleep 1000
SVC
cat > /etc/slinit.d/test-337-f <<'SVC'
type = process
command = /bin/sh -c "sleep 1; exit 1"
on-failure: test-337-h
SVC
cat > /etc/slinit.d/test-337-ok <<'SVC'
type = process
command = /bin/sh -c "sleep 1; exit 0"
on-failure: test-337-h
SVC

slinitctl --system start test-337-b >/dev/null 2>&1
slinitctl --system start test-337-a >/dev/null 2>&1
wait_for_service test-337-b "STOPPED" 10
assert_service_state test-337-b "STOPPED" "starting a stopped b, which it conflicts with"
assert_service_state test-337-a "STARTED" "a is up"
slinitctl --system start test-337-b >/dev/null 2>&1
wait_for_service test-337-a "STOPPED" 10
assert_service_state test-337-a "STOPPED" "starting b stopped a: either side counts"
slinitctl --system stop test-337-b >/dev/null 2>&1

slinitctl --system --no-wait start test-337-ok >/dev/null 2>&1
sleep 3
assert_service_state test-337-h "STOPPED" "a clean exit starts no on-failure service"
slinitctl --system --no-wait start test-337-f >/dev/null 2>&1
wait_for_service test-337-h "STARTED" 10
assert_service_state test-337-h "STARTED" "a failed service started its on-failure service"
slinitctl --system stop test-337-h >/dev/null 2>&1

test_summary
