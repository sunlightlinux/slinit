#!/bin/sh
# Test: service templates with @ argument substitution.
# Validates: name@arg instantiation, $1 variable expansion in command.

wait_for_service "boot" "STARTED" 10

# The template file "tmpl" is already in /etc/slinit.d/ (from .d/ dir).
# Start an instance with argument "alpha"
slinitctl --system start tmpl@alpha 2>&1
wait_for_service "tmpl@alpha" "STARTED" 15

sleep 2

# Verify $1 was substituted with "alpha"
result=$(cat /tmp/tmpl-alpha-result 2>/dev/null)
assert_eq "$result" "instance=alpha" "\$1 expanded to 'alpha'"

# Start a second instance with argument "beta"
slinitctl --system start tmpl@beta 2>&1
wait_for_service "tmpl@beta" "STARTED" 15

sleep 2

result2=$(cat /tmp/tmpl-beta-result 2>/dev/null)
assert_eq "$result2" "instance=beta" "\$1 expanded to 'beta'"

# Both instances should be running independently
assert_service_state "tmpl@alpha" "STARTED" "tmpl@alpha is STARTED"
assert_service_state "tmpl@beta" "STARTED" "tmpl@beta is STARTED"

# Verify they appear in the service list
list=$(slinitctl --system list 2>&1)
assert_contains "$list" "tmpl@alpha" "tmpl@alpha in service list"
assert_contains "$list" "tmpl@beta" "tmpl@beta in service list"

test_summary
