#!/bin/sh
# Test: slinitctl list shows all loaded services with correct states.
# Validates: control socket, list command, service state reporting.

output=$(slinitctl --system list 2>&1)

assert_contains "$output" "boot" "list contains boot"
assert_contains "$output" "system-init" "list contains system-init"
assert_contains "$output" "test-runner" "list contains test-runner"

# boot and system-init should show started indicator [+] or {+}
assert_contains "$output" "+" "at least one service shows started indicator"

test_summary
