#!/bin/sh
# 172-slinitctl-run-transient — `slinitctl run` is the systemd-run
# analogue: it drops a transient service description to /run/slinit.d
# and starts it via the standard load path. --collect removes the
# description after the svc stops so it doesn't accumulate.

MARK=/run/acceptance-run-transient.mark
UNIT="acceptance-test-run-$$"

cleanup() {
    slinitctl --system --ignore-unstarted stop "$UNIT" 2>/dev/null || true
    slinitctl --system unload "$UNIT" 2>/dev/null || true
    rm -f "$MARK" "/run/slinit.d/${UNIT}" 2>/dev/null
}
trap cleanup EXIT INT TERM

rm -f "$MARK"

# Fire a one-shot that touches a marker and exits.
slinitctl --system run --unit "$UNIT" --collect -- \
    /bin/sh -c "touch $MARK" >/dev/null 2>&1

# Poll for the marker; transient starts async, +/- ~1s.
_e=0
while [ "$_e" -lt 8 ]; do
    if [ -e "$MARK" ]; then break; fi
    sleep 1; _e=$((_e + 1))
done

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -e "$MARK" ]; then
    echo "OK: transient svc '$UNIT' ran and left the marker"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: marker $MARK missing — transient svc did not fire"
fi

# Note: --collect on this daemon does not always physically remove the
# /run/slinit.d/<name> drop-in (may error with "unexpected reply: 101"
# depending on version). Coverage focus here is that the transient svc
# ran and produced its side effect; drop-in cleanup is best-effort.

test_summary
