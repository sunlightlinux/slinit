#!/bin/sh
# 179-slinitctl-is-newer-older-than — `is-newer-than FILE-A FILE-B` and
# `is-older-than FILE-A FILE-B` compare mtimes. Used by scripted svcs to
# decide whether they need to regenerate a derived file. Verifies the
# operator's contract: exit code semantics reflect the comparison.

A=/tmp/acceptance-test-inot-a
B=/tmp/acceptance-test-inot-b

cleanup() { rm -f "$A" "$B"; }
trap cleanup EXIT INT TERM

touch "$A"
sleep 1
touch "$B"

# B is newer than A.
assert_exit_code "slinitctl is-newer-than $B $A" 0 \
    "is-newer-than B A → 0 (B is newer)"

# A is older than B — same fact from the other angle.
assert_exit_code "slinitctl is-older-than $A $B" 0 \
    "is-older-than A B → 0 (A is older)"

# The inverse comparisons should be non-zero.
assert_exit_code "slinitctl is-newer-than $A $B" 1 \
    "is-newer-than A B → non-zero (A is not newer)"

assert_exit_code "slinitctl is-older-than $B $A" 1 \
    "is-older-than B A → non-zero (B is not older)"

test_summary
