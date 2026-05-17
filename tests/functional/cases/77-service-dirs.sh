#!/bin/sh
# Test: systemd-style auto-managed service directories.
# Validates: runtime-directory (/run) and state-directory (/var/lib) are
# created before the service runs; on stop the runtime one is removed
# while the state one persists.

wait_for_service "dir-svc" "STARTED" 10
assert_eq "$(cat /tmp/dir-marker 2>/dev/null)" "up" "service body ran"

# Both directories must exist while the service is up.
[ -d /run/slinit-rt ] && rt=yes || rt=no
[ -d /var/lib/slinit-st ] && st=yes || st=no
assert_eq "$rt" "yes" "runtime-directory /run/slinit-rt created"
assert_eq "$st" "yes" "state-directory /var/lib/slinit-st created"

# Stop the service: runtime dir is volatile, state dir persists.
slinitctl --system stop dir-svc >/dev/null 2>&1
wait_for_service "dir-svc" "STOPPED" 10

[ -d /run/slinit-rt ] && rt2=yes || rt2=no
[ -d /var/lib/slinit-st ] && st2=yes || st2=no
assert_eq "$rt2" "no" "runtime-directory removed on stop"
assert_eq "$st2" "yes" "state-directory persists after stop"

test_summary
