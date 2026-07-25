#!/bin/sh
# 193-slinit-monitor-e2e — slinit-monitor subscribes to the daemon's
# SERVICEEVENT stream. Trigger a state change on a live svc and
# verify the monitor's -c command fired with the right substitutions.

SVC="acceptance-test-monitor-e2e"
LOG=/tmp/acceptance-monitor.log
HOOK=/tmp/acceptance-monitor-hook.sh

cleanup() {
    kill "$MON_PID" 2>/dev/null || true
    svc_remove "$SVC"
    rm -f "$LOG" "$HOOK"
}
trap cleanup EXIT INT TERM

rm -f "$LOG"

# The monitor's -c runs argv-split (no shell). Wrap in a tiny helper
# script so we can redirect stdout to the log — the standard escape
# hatch per feedback-acceptance-gotchas.
cat > "$HOOK" <<'EOF'
#!/bin/sh
echo "svc=$1 state=$2" >> /tmp/acceptance-monitor.log
EOF
chmod +x "$HOOK"

svc_deploy "$SVC" <<EOF
type = process
command = /bin/sh -c 'exec sleep 600'
restart = false
EOF

# Fire the monitor in the background BEFORE we cause state changes,
# so we capture the transitions.
slinit-monitor --system -c "$HOOK %n %s" "$SVC" >/dev/null 2>&1 &
MON_PID=$!
sleep 1

# Trigger state changes.
slinitctl --system start "$SVC" >/dev/null
wait_for_service "$SVC" "STARTED" 10
slinitctl --system stop "$SVC" >/dev/null
wait_for_service "$SVC" "STOPPED" 10

# Give the monitor a moment to flush.
sleep 1

_TESTS_RUN=$((_TESTS_RUN + 1))
if grep -q "svc=$SVC state=started" "$LOG" 2>/dev/null; then
    echo "OK: monitor captured 'started' transition"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: 'started' transition missing from monitor log"
fi

_TESTS_RUN=$((_TESTS_RUN + 1))
if grep -q "svc=$SVC state=stopped" "$LOG" 2>/dev/null; then
    echo "OK: monitor captured 'stopped' transition"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: 'stopped' transition missing from monitor log"
fi

test_summary
