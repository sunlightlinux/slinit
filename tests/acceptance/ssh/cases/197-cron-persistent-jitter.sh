#!/bin/sh
# 197-cron-persistent-jitter — cron-persistent + cron-randomized-delay
# directives on a periodic svc. Full catch-up-after-downtime semantics
# for cron-persistent would need a daemon restart (not practical in
# a live SSH suite — the daemon IS slinit PID 1), so we verify:
#   1. cron-command with cron-interval + cron-randomized-delay +
#      cron-persistent parses and the svc runs.
#   2. The cron sub-task actually fires within the interval + jitter
#      window, proving the scheduler wired everything correctly.

SVC="acceptance-test-cronjit"
MARK=/tmp/acceptance-cronjit.log

cleanup() { svc_remove "$SVC"; rm -f "$MARK"; }
trap cleanup EXIT INT TERM
rm -f "$MARK"

svc_deploy "$SVC" <<EOF
type = process
command = /bin/sh -c 'exec sleep 600'
restart = false
# cron-command fires periodically while the svc is running. Interval
# is 3s with a 1s jitter — a fire should land within 4s of the last.
# cron-persistent = yes flags the fire history to survive daemon
# restarts (only observable across restart; here we just prove it
# parses + starts).
cron-command          = /bin/sh -c 'date >> $MARK'
cron-interval         = 3
cron-randomized-delay = 1s
cron-persistent       = yes
EOF

slinitctl --system start "$SVC" >/dev/null
wait_for_service "$SVC" "STARTED" 10 || { test_summary; return; }

# Give the cron scheduler enough time to fire at least twice within
# the interval + jitter window. 12s covers 3 fires at 3s + 1s jitter.
sleep 12

_hits=$(wc -l < "$MARK" 2>/dev/null || echo 0)
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$_hits" -ge 2 ]; then
    echo "OK: cron-command fired $_hits time(s) in ~12s (interval=3 + jitter=1s)"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: expected >=2 cron fires, got $_hits"
fi

test_summary
