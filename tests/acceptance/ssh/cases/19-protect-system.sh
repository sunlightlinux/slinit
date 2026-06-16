#!/bin/sh
# 19-protect-system — `protect-system = strict` must make the system tree
# read-only from inside the service's mount namespace. Probe: have the
# service attempt to create a file under /usr and report the exit code.

SVC="acceptance-test-protect-system"
MARK="/run/acceptance-test-protect-system.result"

trap 'svc_remove "$SVC"; rm -f "$MARK"' EXIT INT TERM

rm -f "$MARK"

# scripted service: try to write, capture exit, exit cleanly so the service
# reaches STARTED rather than FAILED (we want to assert from the side-channel
# file, not on the start state).
svc_deploy "$SVC" <<EOF
type = scripted
protect-system = strict
command = /bin/sh -c 'if touch /usr/.acceptance-probe 2>/dev/null; then echo writable > $MARK; rm -f /usr/.acceptance-probe; else echo readonly > $MARK; fi; exit 0'
restart = false
EOF

slinitctl --system start "$SVC" >/dev/null 2>&1
# Scripted services transition to STARTED once the command exits 0.
wait_for_service "$SVC" "STARTED" 10 || true

# Give the redirect a tick to settle.
sleep 1

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -r "$MARK" ]; then
    _result="$(cat "$MARK")"
    if [ "$_result" = "readonly" ]; then
        echo "OK: /usr is read-only inside the service namespace"
    else
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: probe wrote '$_result' (expected 'readonly')"
    fi
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: probe marker $MARK not created"
fi

# And the host /usr still mounted normally — sanity check that the test
# didn't leak the read-only remount.
_TESTS_RUN=$((_TESTS_RUN + 1))
if touch /usr/.acceptance-sanity 2>/dev/null; then
    rm -f /usr/.acceptance-sanity
    echo "OK: host /usr remains writable"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: host /usr became read-only too — namespace leak?"
fi

test_summary
