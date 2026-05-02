#!/bin/sh
# Test: load-options export-passwd-vars and export-service-name.
# Validates: USER/HOME/SHELL derived from run-as, DINIT_SERVICENAME env var.

wait_for_service "loadopt-svc" "STARTED" 10

sleep 2

# Read the process environment (written from /proc/self/environ)
result=$(cat /tmp/loadopt-result 2>/dev/null)

# export-passwd-vars should set USER from run-as
assert_contains "$result" "USER=root" "USER exported from run-as"

# export-passwd-vars should set HOME
assert_contains "$result" "HOME=" "HOME exported from run-as"

# export-service-name should set DINIT_SERVICENAME (dinit compat)
assert_contains "$result" "DINIT_SERVICENAME=loadopt-svc" "DINIT_SERVICENAME exported"

assert_service_state "loadopt-svc" "STARTED" "loadopt-svc is STARTED"

test_summary
