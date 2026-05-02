#!/bin/sh
# Test: healthcheck-command detects unhealthy service.
# Validates: healthcheck-command, healthcheck-interval, healthcheck-delay,
#            healthcheck-max-failures, unhealthy-command execution.

wait_for_service "health-svc" "STARTED" 10

# The service starts healthy. After 3s it creates /tmp/health-fail which
# makes the healthcheck fail. After max-failures (2) consecutive failures,
# the unhealthy-command fires.

# Verify service is initially healthy
assert_service_state "health-svc" "STARTED" "health-svc initially STARTED"

# Wait for the service to become unhealthy:
# 3s delay before fail marker + 2s healthcheck-delay + 2x 1s interval + margin
sleep 10

# Verify unhealthy-command ran
result=$(cat /tmp/unhealthy-marker 2>/dev/null)
assert_eq "$result" "unhealthy-fired" "unhealthy-command executed"

test_summary
