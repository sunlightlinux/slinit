#!/bin/sh
# Test: protect-clock blocks clock_settime/adjtime/settimeofday/
# adjtimex via seccomp.

SVC="test-pclock"
OUT="/tmp/functional-pclock-out"

cat > "/etc/slinit.d/$SVC" <<EOF
type = process
protect-clock = yes
command = /bin/sh -c 'date -s "@$(date +%s)" 2>$OUT >/dev/null; while :; do sleep 60; done'
restart = no
EOF

slinitctl --system start "$SVC" 2>/dev/null
wait_for_service "$SVC" STARTED 10
assert_service_state "$SVC" "STARTED" "service reached STARTED"

# STARTED is emitted right after fork for type=process without notify,
# which races with slinit-runner's seccomp Install() — /proc/PID/status
# can briefly show Seccomp=0 in the window between fork and the
# runner's install. Poll for up to 2s (same shape as 116-lock-personality).
_pid=$(slinitctl --system status "$SVC" 2>/dev/null | awk '/PID:/ { print $2; exit }')
_seccomp=0
for _ in $(seq 50); do   # up to 10 s: TCG can be slow to exec past the runner
    _seccomp=$(awk '/^Seccomp:/ { print $2 }' "/proc/$_pid/status" 2>/dev/null)
    [ "$_seccomp" = "2" ] && break
    sleep 0.2
done
assert_eq "$_seccomp" "2" "seccomp filter (mode 2) installed on child"

# Since 3.18.0 protect-clock refuses with EPERM, as systemd's does,
# rather than killing date, so date must have reported the refusal.
# This used to accept anything at all (an empty stderr, from the kill,
# or any message), so it could not fail.
_err=$(cat "$OUT" 2>/dev/null)
_TESTS_RUN=$((_TESTS_RUN + 1))
case "$_err" in
    *"not permitted"*)
        echo "OK: clock_settime refused with EPERM ($_err)" ;;
    *)
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: date -s was not refused with EPERM; stderr: '${_err}'" ;;
esac

test_summary
