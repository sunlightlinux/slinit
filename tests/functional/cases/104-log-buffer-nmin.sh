#!/bin/sh
# Test: logfile-min-files recovers from a full filesystem
# (log-buffer-size is parse coverage here).
#
# The logfile lives on a 64 KiB tmpfs. The service writes about 100 KB
# and then a final marker. Rotation at 8 KiB with up to 10 old files
# fills the filesystem before rotation would prune anything, so a write
# hits ENOSPC. With logfile-min-files = 1 the rotator deletes old files
# down to one and retries, and the marker reaches the current file;
# without it the write is lost. (The case used to check only that a
# single line reached the file.)

SVC="test-logbufnmin"
LOG_DIR=/tmp/functional-logbufnmin
LOG_FILE="$LOG_DIR/current"

mkdir -p "$LOG_DIR"
if ! mount -t tmpfs -o size=64k tmpfs "$LOG_DIR"; then
    skip_case "cannot mount a small tmpfs"
fi

cat > "/etc/slinit.d/$SVC" <<EOF
type = process
command = /bin/sh -c "i=0; while [ \$\$i -lt 1000 ]; do echo LINE-\$\$i-padding-padding-padding-padding-padding-padding-padding-padding-padding; i=\$\$((i + 1)); done; echo FINAL-MARK; exec sleep 60"
restart = false
logfile = $LOG_FILE
log-buffer-size = 8192
logfile-max-size = 8192
logfile-max-files = 10
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
while [ "$_e" -lt 10 ] && ! grep -q FINAL-MARK "$LOG_FILE" 2>/dev/null; do
    sleep 0.5; _e=$((_e + 1))
done
echo "INFO: $(ls "$LOG_DIR" | wc -l) files on the tmpfs, $(df -k "$LOG_DIR" | awk 'NR == 2 { print $4 }') KiB free"
assert_contains "$(cat "$LOG_FILE" 2>/dev/null)" "FINAL-MARK" \
    "the last line was written after the filesystem filled"

slinitctl --system stop "$SVC" >/dev/null 2>&1
umount "$LOG_DIR" 2>/dev/null
test_summary
