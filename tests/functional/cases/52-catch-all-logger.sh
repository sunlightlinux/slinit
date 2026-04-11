#!/bin/sh
# Test: catch-all logger captures early boot output to persistent log file.
# Validates: CatchAllLogger pipe redirect, file persistence, console tee.

# The catch-all logger writes to /run/slinit/catch-all.log by default.
CATCH_ALL="/run/slinit/catch-all.log"

sleep 2

# Verify the catch-all log file exists
if [ -f "$CATCH_ALL" ]; then
    assert_eq "1" "1" "catch-all log file exists"
else
    assert_eq "0" "1" "catch-all log file exists"
fi

# Verify it contains slinit boot messages (the logger captures stdout/stderr)
_content=$(cat "$CATCH_ALL" 2>/dev/null)
assert_contains "$_content" "slinit" "catch-all log contains slinit output"

# Verify lines have timestamps (ISO8601-style: YYYY-MM-DDTHH:MM:SS)
_first_line=$(head -1 "$CATCH_ALL" 2>/dev/null)
case "$_first_line" in
    20[0-9][0-9]-*)
        assert_eq "1" "1" "catch-all log lines have timestamps"
        ;;
    *)
        assert_eq "0" "1" "catch-all log lines have timestamps (got: $_first_line)"
        ;;
esac

test_summary
