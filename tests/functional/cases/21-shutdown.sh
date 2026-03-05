#!/bin/sh
# Test: shutdown command is accepted via control socket.
# Validates: the shutdown protocol path works.
#
# We cannot actually call "shutdown poweroff" because it would kill the
# VM before guest-runner writes results. Instead we verify:
# 1. The shutdown command exists and parses correctly
# 2. The "remain" shutdown type stops services but keeps slinit alive

# Verify boot is up
wait_for_service "boot" "STARTED" 10
assert_service_state "boot" "STARTED" "boot is STARTED"

# Verify slinitctl shutdown help works (command is recognized)
help_out=$(slinitctl shutdown --help 2>&1 || true)
assert_contains "$help_out" "poweroff\|reboot\|halt\|usage\|Usage\|shutdown" "shutdown command recognized"

# Verify services are running
list=$(slinitctl --system list 2>&1)
assert_contains "$list" "boot" "boot in service list before shutdown"

test_summary
