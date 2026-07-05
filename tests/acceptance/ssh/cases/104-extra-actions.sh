#!/bin/sh
# 104-extra-actions — `extra-command` (available in any state) and
# `extra-started-command` (only when STARTED) custom actions.

SVC="${ACCEPTANCE_NS_PREFIX}extra"
LOG="/tmp/acceptance-extra.log"

cleanup() {
    svc_remove "$SVC"
    rm -f "$LOG"
}
trap cleanup EXIT INT TERM
cleanup

svc_deploy "$SVC" <<EOF
type = process
command = /bin/sh -c 'while true; do sleep 60; done'
extra-command = dump /bin/sh -c 'echo dump-fired >> $LOG'
extra-started-command = probe /bin/sh -c 'echo probe-fired >> $LOG'
restart = no
EOF

# list-actions shows the actions we declared.
_TESTS_RUN=$((_TESTS_RUN + 1))
_actions=$(slinitctl --system list-actions "$SVC" 2>&1)
case "$_actions" in
    *dump*probe*|*probe*dump*)
        echo "OK: list-actions returns both custom actions"
        ;;
    *)
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: list-actions output: $_actions"
        test_summary
        exit 0
        ;;
esac

# Before start: extra-command (dump) is invokable, extra-started-command
# (probe) refuses because service isn't STARTED yet.
slinitctl --system action "$SVC" dump 2>/dev/null
sleep 0.5
_TESTS_RUN=$((_TESTS_RUN + 1))
if grep -q "dump-fired" "$LOG" 2>/dev/null; then
    echo "OK: extra-command 'dump' ran while service was STOPPED"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: dump did not fire — log: $(cat $LOG 2>&1)"
fi

_TESTS_RUN=$((_TESTS_RUN + 1))
if slinitctl --system action "$SVC" probe 2>&1 | grep -qi "not.*started\|refused\|state"; then
    echo "OK: extra-started-command 'probe' refused while STOPPED"
else
    # Some builds silently no-op instead of erroring; accept both
    # provided the log line doesn't appear.
    if ! grep -q "probe-fired" "$LOG"; then
        echo "OK: probe silently skipped (no log entry, no state error)"
    else
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: probe fired while service was STOPPED"
    fi
fi

# Start the service.
slinitctl --system start "$SVC" 2>/dev/null
wait_for_service "$SVC" STARTED 10
assert_eq "$(svc_state "$SVC")" "STARTED" "extra-actions service reached STARTED"

# Both actions are now invokable.
slinitctl --system action "$SVC" probe 2>/dev/null
sleep 0.5
_TESTS_RUN=$((_TESTS_RUN + 1))
if grep -q "probe-fired" "$LOG"; then
    echo "OK: extra-started-command 'probe' ran while STARTED"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: probe did not fire — log: $(cat $LOG 2>&1)"
fi

# dump is still callable while STARTED (extra-command is unconditional).
: > "$LOG"
slinitctl --system action "$SVC" dump 2>/dev/null
sleep 0.5
_TESTS_RUN=$((_TESTS_RUN + 1))
if grep -q "dump-fired" "$LOG"; then
    echo "OK: extra-command 'dump' ran while STARTED"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: dump did not fire on second invocation"
fi

# Unknown action rejected.
_TESTS_RUN=$((_TESTS_RUN + 1))
if slinitctl --system action "$SVC" nonsense >/dev/null 2>&1; then
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: unknown action should have errored"
else
    echo "OK: unknown action name rejected"
fi

test_summary
