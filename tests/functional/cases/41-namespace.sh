#!/bin/sh
# Test: namespace isolation (PID namespace, user namespace with UID/GID mapping).
# Validates: namespace-pid, namespace-mount, namespace-user, namespace-uid-map, namespace-gid-map.
#
# What the probes prove. Both used to pass with the namespaces off:
# every process has an NSpid line, and uid 0 maps to uid 0 either way.
#   - PID ns: NSpid lists one PID per namespace level. A process in a
#     child PID namespace has two (host PID, ns PID), so the line is
#     "NSpid: <host> <ns>" — three awk fields. Without the namespace it
#     is two.
#   - user ns: /proc/self/uid_map shows the mapping the process lives
#     under. The configured 0:0:65536 reads "0 0 65536"; the initial
#     namespace's identity map is "0 0 4294967295". The third field
#     tells them apart.

wait_for_service "ns-pid-svc" "STARTED" 10
wait_for_service "ns-user-svc" "STARTED" 10

# Give processes a moment to write their results
sleep 2

pid_result=$(cat /tmp/ns-pid-result 2>/dev/null)
echo "  diag: '$pid_result'"
assert_contains "$pid_result" "NSpid" "probe read its NSpid line"
nf=$(echo "$pid_result" | awk '{ print NF; exit }')
assert_eq "$nf" "3" "PID namespace active (NSpid has host + namespace PID)"

# Service should be running
assert_service_state "ns-pid-svc" "STARTED" "ns-pid-svc is STARTED"

# User namespace: uid 0 inside, and the map is the configured one.
user_result=$(cat /tmp/ns-user-result 2>/dev/null)
assert_eq "$user_result" "0" "user namespace maps to uid 0"
uid_map=$(cat /tmp/ns-user-uidmap 2>/dev/null)
echo "  diag: uid_map '$uid_map'"
map_len=$(echo "$uid_map" | awk '{ print $3; exit }')
assert_eq "$map_len" "65536" "uid_map is the configured 0:0:65536 (not the identity map)"

assert_service_state "ns-user-svc" "STARTED" "ns-user-svc is STARTED"

test_summary
