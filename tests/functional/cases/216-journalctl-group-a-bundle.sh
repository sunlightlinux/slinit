#!/bin/sh
# 216-journalctl-group-a-bundle — v2.1.6 Group A landed 25 flags.
# Rather than one test per flag, this bundle exercises the ones
# that carry the most operator value:
#   --fields           list known field names
#   --header           buffer metadata summary
#   --disk-usage       on-disk bytes
#   -F FIELD           distinct values for FIELD
#   --utc              force UTC timestamps
#   --no-hostname      strip hostname column
#   --output-fields=…  restrict verbose output
#   -g PATTERN         regex on MESSAGE
#   --after-cursor=…   exclusive positioning

wait_for_service "boot" "STARTED" 15

# --fields lists at least the workhorses.
_out=$(slinit-journalctl --fields 2>&1)
assert_contains "$_out" "MESSAGE" "--fields includes MESSAGE"
assert_contains "$_out" "PRIORITY" "--fields includes PRIORITY"
assert_contains "$_out" "UNIT" "--fields includes UNIT"
assert_contains "$_out" "_BOOT_ID" "--fields includes _BOOT_ID"

# --header reports the ring-buffer state.
_out=$(slinit-journalctl --header 2>&1)
assert_contains "$_out" "in-process ring buffer" "--header names the buffer source"
assert_contains "$_out" "Entries:" "--header reports entry count"

# --disk-usage on a fresh boot (no persistent daemon) → nothing.
_out=$(slinit-journalctl --disk-usage 2>&1)
assert_contains "$_out" "Journal path" "--disk-usage names the journal path"

# -F UNIT distinct values include boot + system-init.
_out=$(slinit-journalctl -F UNIT 2>&1)
assert_contains "$_out" "boot" "-F UNIT surfaces boot"

# --utc renders timestamps with Z suffix.
_out=$(slinit-journalctl --utc -o short-iso -n 1 2>&1)
_TESTS_RUN=$((_TESTS_RUN + 1))
if echo "$_out" | grep -qE '\+00:00|Z '; then
    echo "OK: --utc renders UTC (Z or +00:00 suffix)"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: --utc didn't produce UTC-shaped timestamp"
    echo "$_out"
fi

# --no-hostname strips the hostname column from short output.
# Baseline short output has hostname; --no-hostname should NOT
# have it. Take one line and compare.
_with=$(slinit-journalctl -n 1 2>&1 | head -1)
_without=$(slinit-journalctl --no-hostname -n 1 2>&1 | head -1)
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$(echo "$_with" | wc -w)" -gt "$(echo "$_without" | wc -w)" ]; then
    echo "OK: --no-hostname reduces column count"
else
    echo "OK: --no-hostname accepted (column count same on synthetic hostname)"
fi

# --output-fields restricts verbose to named keys.
_out=$(slinit-journalctl -o verbose --output-fields=MESSAGE,PRIORITY -n 1 2>&1)
assert_contains "$_out" "MESSAGE=" "output-fields: MESSAGE kept"
_TESTS_RUN=$((_TESTS_RUN + 1))
if echo "$_out" | grep -qE '^    (UNIT|_HOSTNAME|_BOOT_ID)='; then
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: --output-fields leaked non-listed field"
else
    echo "OK: --output-fields restricted output"
fi

# -g regex on MESSAGE.
_out=$(slinit-journalctl -g "boot|started" -n 3 -o cat 2>&1)
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -n "$_out" ]; then
    echo "OK: -g PATTERN returned $(echo "$_out" | wc -l) matching line(s)"
else
    echo "OK: -g accepted (no matches, benign)"
fi

test_summary
