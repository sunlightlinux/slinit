#!/bin/sh
# 34-failure-action — `failure-action = ACTION` declares the system-wide
# action on a service failure. Valid values per pkg/service/types.go's
# ParseSystemAction: none, reboot, poweroff, exit (alias halt).
#
# Behavioural probe is intentionally narrow: we use `failure-action = none`
# with an always-failing service, assert that the service ends FAILED and
# the system does NOT reboot/poweroff. Then we confirm via slinit-check
# that all three non-default values (reboot/poweroff/exit) parse cleanly —
# behavioural probes for those would shut the test VM down.

SVC="acceptance-test-failaction"
SLINIT_CHECK_TMP="/tmp/acceptance-test-failaction.cfg"

cleanup() {
    svc_remove "$SVC"
    rm -f "$SLINIT_CHECK_TMP"
}
trap cleanup EXIT INT TERM

svc_deploy "$SVC" <<EOF
type = scripted
failure-action = none
command = /bin/sh -c 'exit 1'
restart = false
EOF

slinitctl --system start "$SVC" 2>/dev/null || true

# Wait for the failure to settle.
_e=0
while [ "$_e" -lt 8 ]; do
    _st=$(svc_state "$SVC")
    case "$_st" in
        FAILED|STOPPED) break ;;
    esac
    sleep 1
    _e=$((_e + 1))
done

# Service must be in a failure-end state. None of FAILED, STOPPED is wrong:
# slinit reports a scripted service whose command exited non-zero as FAILED.
_TESTS_RUN=$((_TESTS_RUN + 1))
case "$_st" in
    FAILED) echo "OK: $SVC in FAILED" ;;
    STOPPED) echo "OK: $SVC in STOPPED (legacy label)" ;;
    *) _TESTS_FAILED=$((_TESTS_FAILED + 1))
       echo "FAIL: $SVC unexpected end state '$_st'" ;;
esac

# Slinit and the daemon must still be running — failure-action=none means
# no system-level reaction.
_TESTS_RUN=$((_TESTS_RUN + 1))
if slinitctl --system list >/dev/null 2>&1; then
    echo "OK: slinit still responsive (no implicit shutdown)"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: slinitctl can't reach the daemon — system action fired?"
fi

# Static parse check for the dangerous variants. slinit-check will only
# refuse if the value can't be parsed; we ignore other diagnostics (deps,
# command paths) by gating only on the "unknown system action" string.
for _action in reboot poweroff exit halt; do
    cat > "$SLINIT_CHECK_TMP" <<EOC
type = scripted
failure-action = $_action
command = /bin/true
EOC
    _out=$(slinit-check "$SLINIT_CHECK_TMP" 2>&1 || true)
    _TESTS_RUN=$((_TESTS_RUN + 1))
    case "$_out" in
        *"unknown system action"*|*"invalid failure-action"*)
            _TESTS_FAILED=$((_TESTS_FAILED + 1))
            echo "FAIL: '$_action' rejected by parser: $_out"
            ;;
        *)
            echo "OK: failure-action=$_action parses"
            ;;
    esac
done

test_summary
