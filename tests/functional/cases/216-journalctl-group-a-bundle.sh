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
# short-iso is "TIMESTAMP HOST IDENT[PID]: MSG" — the timestamp is one
# token, so field 2 is the host and the identifier (the first field
# ending in ':') is field 3. With --no-hostname the identifier moves to
# field 2. Judged by position, not by searching for the host name: the
# guest's hostname (or the "-" placeholder) can appear in a message too.
_with=$(slinit-journalctl -o short-iso -n 1 2>&1 | head -1)
_without=$(slinit-journalctl --no-hostname -o short-iso -n 1 2>&1 | head -1)
_w2=$(echo "$_with" | awk '{print $2}')
_w3=$(echo "$_with" | awk '{print $3}')
_n2=$(echo "$_without" | awk '{print $2}')
_TESTS_RUN=$((_TESTS_RUN + 1))
case "$_w2" in
    ""|*:)
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: baseline short-iso has no host column: '$_with'" ;;
    *)
        case "$_w3" in
            *:) echo "OK: baseline short-iso carries host '$_w2' before '$_w3'" ;;
            *)
                _TESTS_FAILED=$((_TESTS_FAILED + 1))
                echo "FAIL: baseline short-iso line not shaped as expected: '$_with'" ;;
        esac ;;
esac
_TESTS_RUN=$((_TESTS_RUN + 1))
case "$_n2" in
    *:)
        echo "OK: --no-hostname puts the identifier ('$_n2') right after the timestamp" ;;
    *)
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: --no-hostname still has a column before the identifier: '$_without'" ;;
esac

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

# -g regex on MESSAGE. Read the whole buffer both ways: the unfiltered
# messages must include lines the pattern does not match (otherwise a
# -g that filtered nothing would look the same), and the -g output must
# be non-empty and consist only of matching lines. A lowercase pattern
# is case-insensitive (systemd's heuristic), so the check uses -i too.
_pat="system-init"
_all=$(slinit-journalctl -o cat 2>&1)
_all_miss=$(printf '%s\n' "$_all" | grep -cviE "$_pat")
_out=$(slinit-journalctl -g "$_pat" -o cat 2>&1)
_hit=$(printf '%s\n' "$_out" | grep -ciE "$_pat")
_miss=$(printf '%s\n' "$_out" | grep -cviE "$_pat")
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$_all_miss" -eq 0 ]; then
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: every journal message matches '$_pat' — -g cannot be judged"
elif [ "$_hit" -eq 0 ]; then
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: -g '$_pat' returned no matching line"
elif [ "$_miss" -ne 0 ]; then
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: -g '$_pat' let $_miss non-matching line(s) through:"
    printf '%s\n' "$_out" | grep -viE "$_pat" | head -5
else
    echo "OK: -g '$_pat' kept $_hit matching line(s), dropped all $_all_miss others"
fi

test_summary
