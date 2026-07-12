#!/bin/sh
# 153-log-sanitize — svlogd -r replacement char + -R extra sanitize
# set. Any control byte in the input is rewritten to the sanitize
# char before landing in the on-disk log. Verified with a printf
# that emits BEL + ESC (both non-printable) in a marker line.

SVC="acceptance-test-logsanitize"
LOG_DIR=/tmp/acceptance-logsanitize.$$
LOG_FILE="$LOG_DIR/current"

cleanup() {
    svc_remove "$SVC"
    rm -rf "$LOG_DIR"
}
trap cleanup EXIT INT TERM

mkdir -p "$LOG_DIR"

# printf '\a' = BEL (0x07), '\033' = ESC (0x1B). log-sanitize=? maps
# every control byte to '?'. If sanitization is off the raw byte
# hits disk and the assertion fails.
# Escape mechanics: heredoc `\\\\a` → on-disk `\\a` → slinit-parsed `\a`
# → sh printf sees `\a` and emits real BEL. Same story for `\033` and
# `\n`. (See case 64's comment for the two-step slinit escape rule.)
svc_deploy "$SVC" <<EOF
type = process
command = /bin/sh -c 'printf "SANMARK\\\\aend\\\\033tail\\\\n"; sleep 30'
restart = false
logfile = $LOG_FILE
log-sanitize = ?
EOF
slinitctl --system start "$SVC" >/dev/null 2>&1
wait_for_service "$SVC" "STARTED" 10 || true
assert_service_state "$SVC" "STARTED" "$SVC STARTED"

_e=0
while [ "$_e" -lt 5 ] && [ ! -s "$LOG_FILE" ]; do
    sleep 1; _e=$((_e + 1))
done
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ ! -s "$LOG_FILE" ]; then
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: log file empty"
    test_summary
    return 1 2>/dev/null || exit 1
fi

# Marker + '?' replacements must all be in the same line.
_line=$(grep 'SANMARK' "$LOG_FILE" | head -1)
assert_contains "$_line" "SANMARK?end?tail" \
    "control bytes rewritten to sanitize char"

# Negative: raw BEL byte must NOT be in the log.
_TESTS_RUN=$((_TESTS_RUN + 1))
if ! grep -q "$(printf '\a')" "$LOG_FILE" 2>/dev/null; then
    echo "OK: raw BEL absent from log"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: raw BEL still present in log"
fi

test_summary
