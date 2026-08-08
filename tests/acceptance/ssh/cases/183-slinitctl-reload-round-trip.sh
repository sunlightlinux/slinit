#!/bin/sh
# 183-slinitctl-reload-round-trip — `reload` re-reads the service file
# from disk. We modify the command on disk, reload, then restart —
# verifying the NEW command ran (not the cached old description).

SVC="acceptance-test-reload"
MARK_OLD=/tmp/acceptance-reload-old.mark
MARK_NEW=/tmp/acceptance-reload-new.mark

cleanup() { svc_remove "$SVC"; rm -f "$MARK_OLD" "$MARK_NEW"; }
trap cleanup EXIT INT TERM
rm -f "$MARK_OLD" "$MARK_NEW"

# --- v1 config -------------------------------------------------------
svc_deploy "$SVC" <<EOF
type = process
command = /bin/sh -c 'touch $MARK_OLD; exec sleep 600'
restart = false
EOF

slinitctl --system start "$SVC" >/dev/null
wait_for_service "$SVC" "STARTED" 10 || { test_summary; return; }
sleep 1

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -e "$MARK_OLD" ]; then
    echo "OK: v1 command ran (old marker present)"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: v1 marker missing"
    test_summary; return
fi

# --- Rewrite the config with a different command, stop, reload, start.
# Reload alone doesn't restart the running process — that's by design.
slinitctl --system stop "$SVC" >/dev/null
wait_for_service "$SVC" "STOPPED" 10

svc_deploy "$SVC" <<EOF
type = process
command = /bin/sh -c 'touch $MARK_NEW; exec sleep 600'
restart = false
EOF

slinitctl --system reload "$SVC" >/dev/null 2>&1
slinitctl --system start "$SVC" >/dev/null
wait_for_service "$SVC" "STARTED" 10
sleep 1

assert_eq "$(test -e $MARK_NEW && echo yes)" "yes" \
    "v2 command (new marker) ran after reload+start"

test_summary
