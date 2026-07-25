#!/bin/sh
# 184-slinitctl-signal-e2e — `slinitctl signal HUP <svc>` delivers
# SIGHUP to the service's main pid. Verify by trapping SIGHUP in the
# svc body and having it increment a counter file. Distinct from
# reload-signal (which is a declarative counterpart tested in 169).

SVC="acceptance-test-signal-e2e"
LOG=/tmp/acceptance-signal-e2e.hits

cleanup() { svc_remove "$SVC"; rm -f "$LOG"; }
trap cleanup EXIT INT TERM
rm -f "$LOG"

svc_deploy "$SVC" <<EOF
type = process
command = /bin/sh -c 'trap "echo hit >> $LOG" HUP; while true; do sleep 1; done'
restart = false
EOF

slinitctl --system start "$SVC" >/dev/null
wait_for_service "$SVC" "STARTED" 10 || { test_summary; return; }

# Signal delivery ×2. The trap only fires between wait() calls, so
# allow up to ~2s per delivery for busybox sh.
slinitctl --system signal HUP "$SVC" >/dev/null
sleep 2
slinitctl --system signal HUP "$SVC" >/dev/null
sleep 2

_hits=$(wc -l < "$LOG" 2>/dev/null || echo 0)
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$_hits" -ge 2 ]; then
    echo "OK: signal HUP delivered $_hits time(s)"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: expected >=2 HUP hits, got $_hits"
fi

test_summary
