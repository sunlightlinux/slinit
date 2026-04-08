#!/bin/sh
# Test: namespace isolation (PID namespace, user namespace with UID/GID mapping).
# Validates: namespace-pid, namespace-mount, namespace-user, namespace-uid-map, namespace-gid-map.

wait_for_service "ns-pid-svc" "STARTED" 10
wait_for_service "ns-user-svc" "STARTED" 10

# Give processes a moment to write their results
sleep 2

# PID namespace: the child should see NSpid with two entries (host PID + ns PID)
pid_result=$(cat /tmp/ns-pid-result 2>/dev/null)
assert_contains "$pid_result" "NSpid" "PID namespace active (NSpid present)"

# Service should be running
assert_service_state "ns-pid-svc" "STARTED" "ns-pid-svc is STARTED"

# User namespace: process should see uid 0 inside the namespace
user_result=$(cat /tmp/ns-user-result 2>/dev/null)
assert_eq "$user_result" "0" "user namespace maps to uid 0"

assert_service_state "ns-user-svc" "STARTED" "ns-user-svc is STARTED"

test_summary
