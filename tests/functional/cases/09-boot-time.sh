#!/bin/sh
# Test: boot-time / analyze command returns timing data.
# Validates: CmdBootTime, BootTimeInfo encoding/decoding.

output=$(slinitctl --system boot-time 2>&1)

assert_contains "$output" "kernel" "boot-time shows kernel time"
assert_contains "$output" "userspace" "boot-time shows userspace time"
assert_contains "$output" "boot" "boot-time references boot service"

test_summary
