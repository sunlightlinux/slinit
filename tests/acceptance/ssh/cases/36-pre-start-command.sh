#!/bin/sh
# 36-pre-start-command — `pre-start-command` runs synchronously before
# the main command is forked. Non-zero exit aborts the start so main
# is never invoked. Matches systemd's ExecStartPre= semantics
# (pkg/service/process.go:982).

SVC="acceptance-test-prestart"
MARK_PRE="/run/acceptance-test-prestart.pre"
MARK_MAIN="/run/acceptance-test-prestart.main"

cleanup() {
    svc_remove "$SVC"
    rm -f "$MARK_PRE" "$MARK_MAIN"
}
trap cleanup EXIT INT TERM

rm -f "$MARK_PRE" "$MARK_MAIN"

# --- Sub-case A: pre-start succeeds → main runs after it -----------------
# Pre-start sleeps 1s then touches its marker; main immediately touches
# its marker. If ordering is correct, MARK_PRE.mtime <= MARK_MAIN.mtime
# AND main runs *after* the 1s pre-start sleep (so wall-clock proves
# the sync wait).
svc_deploy "$SVC" <<EOF
type = process
pre-start-command = /bin/sh -c 'sleep 1; touch $MARK_PRE'
command = /bin/sh -c 'touch $MARK_MAIN; while :; do sleep 60; done'
restart = false
EOF

_t0=$(date +%s)
slinitctl --system start "$SVC" >/dev/null 2>&1
wait_for_service "$SVC" "STARTED" 10 || true
_t1=$(date +%s)
assert_service_state "$SVC" "STARTED" "$SVC STARTED (pre-start success)"

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -e "$MARK_PRE" ] && [ -e "$MARK_MAIN" ]; then
    echo "OK: both markers present (pre + main)"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: markers missing (pre=$([ -e "$MARK_PRE" ] && echo y || echo n) main=$([ -e "$MARK_MAIN" ] && echo y || echo n))"
fi

# Wall-clock must be >=1s — proves pre-start was awaited synchronously.
_TESTS_RUN=$((_TESTS_RUN + 1))
_elapsed=$((_t1 - _t0))
if [ "$_elapsed" -ge 1 ]; then
    echo "OK: STARTED took ${_elapsed}s (>=1s, pre-start was awaited)"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: STARTED took ${_elapsed}s (expected >=1s for pre-start)"
fi

svc_remove "$SVC"
rm -f "$MARK_PRE" "$MARK_MAIN"

# --- Sub-case B: pre-start fails → main is never invoked ---------------
svc_deploy "$SVC" <<EOF
type = process
pre-start-command = /bin/sh -c 'exit 5'
command = /bin/sh -c 'touch $MARK_MAIN; while :; do sleep 60; done'
restart = false
EOF

slinitctl --system start "$SVC" >/dev/null 2>&1
sleep 2  # give slinit a moment to attempt + give up

_TESTS_RUN=$((_TESTS_RUN + 1))
_st=$(svc_state "$SVC")
case "$_st" in
    STOPPED|FAILED|"")
        echo "OK: pre-start failure left $SVC in '$_st' (didn't reach STARTED)"
        ;;
    *)
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: pre-start failed but $SVC is '$_st' (expected STOPPED/FAILED)"
        ;;
esac

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -e "$MARK_MAIN" ]; then
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: main ran despite pre-start failure ($MARK_MAIN exists)"
else
    echo "OK: main was never invoked after pre-start failure"
fi

test_summary
