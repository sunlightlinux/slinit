#!/bin/sh
# Test: ready-notification = sd-notify, and a systemd Type=notify unit.
#
# A service with sd-notify readiness gets $NOTIFY_SOCKET and is STARTED
# only when it sends READY=1 (here from a shell, with slinit-notify);
# before 4.1 slinit read nothing but FDSTORE= from that socket, and a
# Type=notify unit was STARTED at its fork. A WATCHDOG=1 sent before
# READY=1 must not count as ready. The same for a systemd unit with
# Type=notify loaded directly from /etc/systemd/system.

cat > /etc/slinit.d/test-333-sd <<'SVC'
type = process
command = /bin/sh -c "sleep 2; slinit-notify WATCHDOG=1; sleep 2; slinit-notify --ready --status=up; exec sleep 1000"
ready-notification = sd-notify
start-timeout = 30
SVC

slinitctl --system --no-wait start test-333-sd >/dev/null 2>&1
sleep 3
assert_service_state test-333-sd "STARTING" "sd-notify: STARTING before READY=1, also after an early WATCHDOG=1"
wait_for_service test-333-sd "STARTED" 10
assert_service_state test-333-sd "STARTED" "sd-notify: STARTED once READY=1 came"
slinitctl --system stop test-333-sd >/dev/null 2>&1

# test-333-unit.service comes from 333-sd-notify-readiness.d/systemd: the
# loader only searches /etc/systemd/system if it exists at boot.
slinitctl --system --no-wait start test-333-unit >/dev/null 2>&1
sleep 1
assert_service_state test-333-unit "STARTING" "Type=notify unit: STARTING until READY=1"
wait_for_service test-333-unit "STARTED" 10
assert_service_state test-333-unit "STARTED" "Type=notify unit: STARTED on READY=1"
slinitctl --system stop test-333-unit >/dev/null 2>&1

test_summary
