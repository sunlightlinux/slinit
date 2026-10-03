#!/bin/sh
# Must be reported PASS. The trivially-true half of the harness check.
assert_eq "a" "a" "a true assertion"
test_summary
