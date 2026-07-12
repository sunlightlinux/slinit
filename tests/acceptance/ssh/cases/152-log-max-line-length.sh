#!/bin/sh
# 152-log-max-line-length — svlogd -l analogue. Lines longer than
# log-max-line-length are truncated with a "..." tail on the same
# line (default discard mode). Verified by producing a known
# overlong line and reading the on-disk log.

SVC="acceptance-test-logmaxline"
LOG_DIR=/tmp/acceptance-logmaxline.$$
LOG_FILE="$LOG_DIR/current"

cleanup() {
    svc_remove "$SVC"
    rm -rf "$LOG_DIR"
}
trap cleanup EXIT INT TERM

mkdir -p "$LOG_DIR"

# One 100-byte marker line; log-max-line-length=32 forces truncation.
# The marker is 8 chars ("MARKPFX ") + 92 x 'x' so a truncated line
# starts with the prefix and does NOT contain the tail of 'x's.
# Note: `\\\\n` in the heredoc → `\\n` on disk → `\n` after slinit's
# escape parse → newline in sh's printf format string. Case 64's
# shared-logger cases document this two-step escape.
_bigx=$(head -c 92 /dev/zero | tr "\0" "x")
svc_deploy "$SVC" <<EOF
type = process
command = /bin/sh -c 'printf "MARKPFX %s\\\\n" "$_bigx"; sleep 30'
restart = false
logfile = $LOG_FILE
log-max-line-length = 32
EOF
slinitctl --system start "$SVC" >/dev/null 2>&1
wait_for_service "$SVC" "STARTED" 10 || true
assert_service_state "$SVC" "STARTED" "$SVC STARTED"

# Give the pipeline 3s to flush.
_e=0
while [ "$_e" -lt 5 ] && [ ! -s "$LOG_FILE" ]; do
    sleep 1; _e=$((_e + 1))
done

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ ! -s "$LOG_FILE" ]; then
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: log file empty at $LOG_FILE"
    test_summary
    return 1 2>/dev/null || exit 1
fi

# Longest line in the file must be ≤ cap+1 bytes (32 content bytes +
# the '+' overflow marker svlogd/LogRotator appends). Any produced
# line at the source was 100 bytes; a line longer than 33 means
# truncation didn't happen.
_maxlen=$(awk '{ if (length > m) m = length } END { print m+0 }' "$LOG_FILE")
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$_maxlen" -le 33 ]; then
    echo "OK: longest line $_maxlen ≤ cap+overflow-marker (33)"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: longest line $_maxlen > cap 33"
    head -5 "$LOG_FILE" | cat -A
fi
assert_contains "$(cat "$LOG_FILE")" "MARKPFX" \
    "truncated line still carries the head marker"

test_summary
