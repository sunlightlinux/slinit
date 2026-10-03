#!/bin/sh
# Must be reported FAIL. This is the one that matters: the harness has
# twice shipped in a state where a failing assertion read as a pass
# (`|| true`, then `| tee` plus `$?`), and nothing in the suite could
# notice, because every case in it is written to pass.
assert_eq "one" "two" "a deliberately false assertion"
test_summary
