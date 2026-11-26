#!/bin/sh
# Test: socket activation passes listening socket to child.
# Validates: socket-listen, LISTEN_FDS and LISTEN_PID, socket file creation.

wait_for_service "sock-svc" "STARTED" 10

# Verify socket file exists
assert_eq "$(test -S /tmp/sock-svc.sock && echo yes || echo no)" "yes" \
    "socket file /tmp/sock-svc.sock exists"

# Verify LISTEN_FDS was set in child
sleep 1
result=$(cat /tmp/sock-result 2>/dev/null)
assert_contains "$result" "LISTEN_FDS=1" "LISTEN_FDS=1 set in child"

# sd_listen_fds() ignores LISTEN_FDS unless LISTEN_PID is the process's
# own pid. It used to be missing altogether.
set -- $(cat /tmp/sock-pid 2>/dev/null)
assert_eq "${1:-unset}" "${2:-}" "LISTEN_PID equals the service's pid"

test_summary
