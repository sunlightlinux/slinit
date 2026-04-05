#!/bin/sh
# Test: multi-service shared logger.
# Validates: shared-logger config, SharedLogMux line prefixing,
#            multiple producers writing to single logger stdin.

wait_for_service "central-logger" "STARTED" 10
wait_for_service "app-one" "STARTED" 10
wait_for_service "app-two" "STARTED" 10

# Give producers time to emit some output
sleep 4

# Check the shared log file written by central-logger
assert_eq "$(test -f /tmp/shared-log && echo yes || echo no)" "yes" \
    "shared log file /tmp/shared-log exists"

log_content=$(cat /tmp/shared-log 2>/dev/null)

# Verify lines from app-one are prefixed
assert_contains "$log_content" "[app-one]" "app-one output prefixed in shared log"

# Verify lines from app-two are prefixed
assert_contains "$log_content" "[app-two]" "app-two output prefixed in shared log"

# Verify actual content is present
assert_contains "$log_content" "hello from app-one" "app-one content in shared log"
assert_contains "$log_content" "hello from app-two" "app-two content in shared log"

# Count total lines — should have at least a few from each
lines=$(wc -l < /tmp/shared-log 2>/dev/null || echo 0)
assert_eq "$([ "$lines" -ge 4 ] && echo yes || echo no)" "yes" \
    "shared log has $lines lines (>= 4 expected)"

test_summary
