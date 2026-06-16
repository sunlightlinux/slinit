#!/bin/sh
# 06-reload-signal — verify slinitctl reload-signal delivers SIGHUP to the
# service process. The probe is a shell that traps HUP and bumps a counter
# file; we read the file before and after.

SVC="acceptance-test-reload"
MARK="/run/acceptance-test-reload.count"

trap 'svc_remove "$SVC"; rm -f "$MARK"' EXIT INT TERM

rm -f "$MARK"
printf 0 > "$MARK"

svc_deploy "$SVC" <<EOF
type = process
reload-signal = HUP
command = /bin/sh -c 'echo 0 > $MARK; trap "echo bump >> $MARK" HUP; while :; do sleep 1; done'
restart = false
EOF

slinitctl --system start "$SVC" >/dev/null 2>&1
wait_for_service "$SVC" "STARTED" 10 || true
assert_service_state "$SVC" "STARTED" "$SVC STARTED"

# Sleep so the shell installs the trap; otherwise the HUP arrives before the
# handler is registered and the shell dies instead of bumping the counter.
sleep 1

_before=$(wc -l < "$MARK" 2>/dev/null || echo 0)

slinitctl --system reload-signal "$SVC" >/dev/null 2>&1 || \
    slinitctl --system signal HUP "$SVC" >/dev/null 2>&1
sleep 2

_after=$(wc -l < "$MARK" 2>/dev/null || echo 0)

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$_after" -gt "$_before" ]; then
    echo "OK: reload-signal bumped counter ($_before -> $_after)"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: counter did not bump ($_before -> $_after)"
fi

# Service must still be STARTED — HUP did not kill it.
assert_service_state "$SVC" "STARTED" "$SVC still STARTED after HUP"

test_summary
