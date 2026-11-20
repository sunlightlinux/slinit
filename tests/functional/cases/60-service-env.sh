#!/bin/sh
# Test: SLINIT_SERVICENAME and SLINIT_SERVICEDSCDIR auto-set env vars.
# Validates: automatic environment injection for every service.

wait_for_service "svcenv-svc" "STARTED" 10

sleep 2

# Read the process environment (from /proc/self/environ)
result=$(cat /tmp/svcenv-result 2>/dev/null)

# SLINIT_SERVICENAME should be set to the service name
assert_contains "$result" "SLINIT_SERVICENAME=svcenv-svc" "SLINIT_SERVICENAME set"

# SLINIT_SERVICEDSCDIR should point to the service directory
assert_contains "$result" "SLINIT_SERVICEDSCDIR=" "SLINIT_SERVICEDSCDIR set"

assert_service_state "svcenv-svc" "STARTED" "svcenv-svc is STARTED"

test_summary
