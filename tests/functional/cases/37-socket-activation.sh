#!/bin/sh
# Test: socket activation passes listening socket to child.
# Validates: socket-listen, LISTEN_FDS env var, socket file creation.

wait_for_service "sock-svc" "STARTED" 10

# Verify socket file exists
assert_eq "$(test -S /tmp/sock-svc.sock && echo yes || echo no)" "yes" \
    "socket file /tmp/sock-svc.sock exists"

# Verify LISTEN_FDS was set in child
sleep 1
result=$(cat /tmp/sock-result 2>/dev/null)
assert_contains "$result" "LISTEN_FDS=1" "LISTEN_FDS=1 set in child"

test_summary
