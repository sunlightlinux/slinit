#!/bin/sh
# 41-cron-interval — `cron-command` runs as a sub-task on every
# `cron-interval`, while the parent service is STARTED. The runner
# starts after STARTED is reached (pkg/service/process.go:582
# startCronIfConfigured), stops cleanly when the service stops.

SVC="acceptance-test-cron-int"
MARK="/run/acceptance-test-cron-int.log"

cleanup() {
    svc_remove "$SVC"
    rm -f "$MARK"
}
trap cleanup EXIT INT TERM

rm -f "$MARK"
: > "$MARK"

# Parent service idles; cron-command appends a timestamp every 2s.
# \$(date) survives slinit's parser unchanged (only $ followed by alpha/
# digit/_/{ is expanded — '(' falls through) and runs at child time.
svc_deploy "$SVC" <<EOF
type = process
command = /bin/sh -c 'while :; do sleep 60; done'
cron-command = /bin/sh -c 'date +%s >> $MARK'
cron-interval = 2s
cron-delay = 500ms
restart = false
EOF

slinitctl --system start "$SVC" >/dev/null 2>&1
wait_for_service "$SVC" "STARTED" 10 || true
assert_service_state "$SVC" "STARTED" "$SVC STARTED"

# Wait ~7s — at 2s interval + 500ms delay we expect 3-4 entries.
sleep 7

_TESTS_RUN=$((_TESTS_RUN + 1))
_n=$(wc -l < "$MARK" 2>/dev/null | tr -d ' ')
if [ -z "$_n" ]; then _n=0; fi
if [ "$_n" -ge 2 ]; then
    echo "OK: cron-command fired $_n times in 7s (expected >=2)"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: cron-command fired only $_n times in 7s (expected >=2)"
fi

# Stop the parent; cron sub-task must also stop. Count entries, wait,
# re-count: stable count proves the cron loop exited with the service.
slinitctl --system stop "$SVC" >/dev/null 2>&1
_t=0
while [ "$_t" -lt 5 ]; do
    if [ "$(svc_state "$SVC")" = "STOPPED" ]; then break; fi
    sleep 1
    _t=$((_t + 1))
done

_before=$(wc -l < "$MARK" 2>/dev/null | tr -d ' ')
sleep 3
_after=$(wc -l < "$MARK" 2>/dev/null | tr -d ' ')

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$_before" = "$_after" ]; then
    echo "OK: cron stopped with the service (count stable at $_before after 3s wait)"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: cron kept firing after stop ($_before → $_after)"
fi

test_summary
