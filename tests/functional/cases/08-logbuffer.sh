#!/bin/sh
# Test: log-type=buffer captures service output, catlog retrieves it.
# Validates: LogBuffer, CmdCatLog, log-buffer-size.

wait_for_service "log-svc" "STARTED" 10

# Give it a moment to produce output
sleep 3

# Retrieve log buffer
output=$(slinitctl --system catlog log-svc 2>&1)
assert_contains "$output" "log-test-marker" "catlog contains expected output"

# Catlog with --clear should return data then clear
output2=$(slinitctl --system catlog --clear log-svc 2>&1)
assert_contains "$output2" "log-test-marker" "catlog --clear returns data"

# After clear, buffer should be empty (or have only new output)
sleep 1
output3=$(slinitctl --system catlog log-svc 2>&1)
assert_not_contains "$output3" "log-test-marker-first" "buffer was cleared"

test_summary
