#!/bin/sh
# 156-log-buffer-nmin — surface-only test for two svlogd config knobs
# that are painful to exercise end-to-end on a live VM:
#   -b log-buffer-size    (memory buffer size before spill)
#   Nmin logfile-min-files (floor for ENOSPC drain; can't trigger
#                          without filling the disk)
# We prove both are accepted at config-parse time via slinit-check
# and that a service using them still starts (i.e. the settings
# don't blow up rotator init).

SVC="acceptance-test-logbufnmin"
LOG_DIR=/tmp/acceptance-logbufnmin.$$
LOG_FILE="$LOG_DIR/current"
SVC_FILE=""

cleanup() {
    svc_remove "$SVC"
    rm -rf "$LOG_DIR"
    [ -n "$SVC_FILE" ] && rm -f "$SVC_FILE"
}
trap cleanup EXIT INT TERM

mkdir -p "$LOG_DIR"

svc_deploy "$SVC" <<EOF
type = process
command = /bin/sh -c 'echo BUFMARK; sleep 30'
restart = false
logfile = $LOG_FILE
log-buffer-size = 8192
logfile-max-files = 3
logfile-min-files = 1
EOF
SVC_FILE="/etc/slinit.d/$SVC"

# slinit-check must accept the file cleanly. slinit-check takes a
# service NAME + -d services-dir (per case 66); passing a full path
# makes it treat the path as a name it can't resolve.
_check=$(slinit-check -d /etc/slinit.d "$SVC" 2>&1)
_rc=$?
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$_rc" -eq 0 ]; then
    echo "OK: slinit-check accepts log-buffer-size + logfile-min-files"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: slinit-check rejected the config (rc=$_rc): $_check"
fi

# Start the service — settings must not break rotator init.
slinitctl --system start "$SVC" >/dev/null 2>&1
wait_for_service "$SVC" "STARTED" 10 || true
assert_service_state "$SVC" "STARTED" "$SVC starts with buf+nmin"

# And a line lands on disk (the rotator's writer still functions).
_e=0
while [ "$_e" -lt 5 ] && ! grep -q BUFMARK "$LOG_FILE" 2>/dev/null; do
    sleep 1; _e=$((_e + 1))
done
assert_contains "$(cat "$LOG_FILE" 2>/dev/null)" "BUFMARK" \
    "marker line reached the rotated log file"

test_summary
