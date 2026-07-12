#!/bin/sh
# Test: log-buffer-size + logfile-min-files parse cleanly and don't
# break rotator init. Surface-only; ENOSPC drain isn't triggerable
# without filling the disk.

SVC="test-logbufnmin"
LOG_DIR=/tmp/functional-logbufnmin
LOG_FILE="$LOG_DIR/current"

mkdir -p "$LOG_DIR"

cat > "/etc/slinit.d/$SVC" <<EOF
type = process
command = /bin/sh -c 'echo BUFMARK; sleep 30'
restart = false
logfile = $LOG_FILE
log-buffer-size = 8192
logfile-max-files = 3
logfile-min-files = 1
EOF

_check=$(slinit-check -d /etc/slinit.d "$SVC" 2>&1)
_rc=$?
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$_rc" -eq 0 ]; then
    echo "OK: slinit-check accepts log-buffer-size + logfile-min-files"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: slinit-check rejected the config (rc=$_rc): $_check"
fi

slinitctl --system start "$SVC" >/dev/null 2>&1
wait_for_service "$SVC" "STARTED" 10 || true
assert_service_state "$SVC" "STARTED" "$SVC starts with buf+nmin"

_e=0
while [ "$_e" -lt 5 ] && ! grep -q BUFMARK "$LOG_FILE" 2>/dev/null; do
    sleep 1; _e=$((_e + 1))
done
assert_contains "$(cat "$LOG_FILE" 2>/dev/null)" "BUFMARK" \
    "marker line reached the rotated log file"

test_summary
