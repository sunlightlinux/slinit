#!/bin/sh
# 61-options-always-chain — `chain-to = B` makes A start B on its way
# out, but the gate is restrictive: A must have exited cleanly with
# code 0 and not be about to restart (record.go:2045). `always-chain =
# yes` widens the gate to "any A stop fires B." This case probes both
# halves.
#
# Sub-case A: always-chain set + A exits 1 → B IS started.
# Sub-case B: default control, A exits 1 → B is NOT started.
#
# B is a one-shot `touch <marker>` so its only observable effect is the
# marker file; we don't need to time-window-poll a "STARTED" state that
# vanishes the moment the touch returns.

SVC_A_AC="acceptance-test-chain-ac-a"
SVC_B_AC="acceptance-test-chain-ac-b"
SVC_A_CTL="acceptance-test-chain-ctl-a"
SVC_B_CTL="acceptance-test-chain-ctl-b"
MARK_AC="/run/acceptance-chain-ac.mark"
MARK_CTL="/run/acceptance-chain-ctl.mark"

cleanup() {
    svc_remove "$SVC_A_AC" "$SVC_B_AC" "$SVC_A_CTL" "$SVC_B_CTL"
    rm -f "$MARK_AC" "$MARK_CTL"
}
trap cleanup EXIT INT TERM

rm -f "$MARK_AC" "$MARK_CTL"

# --- Sub-case A: always-chain = yes, A exits 1 → B fires -----------------
svc_deploy "$SVC_B_AC" <<EOF
type = process
command = /bin/sh -c 'touch $MARK_AC; exit 0'
restart = false
EOF

svc_deploy "$SVC_A_AC" <<EOF
type = process
options = always-chain
chain-to = $SVC_B_AC
command = /bin/sh -c 'exit 1'
restart = false
EOF

# `start --no-wait` to avoid blocking on the failed transition. We're
# interested in the chain side-effect, not A's exit code reaching the
# controller.
slinitctl --system --no-wait start "$SVC_A_AC" >/dev/null 2>&1

# Poll for the chain side-effect. A exits within ~100ms; the chain
# fires inside the stopping handler; B's touch is sub-millisecond.
# 6s tolerates QEMU scheduling jitter without hiding real failures.
_e=0
while [ "$_e" -lt 6 ]; do
    if [ -e "$MARK_AC" ]; then break; fi
    sleep 1
    _e=$((_e + 1))
done

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -e "$MARK_AC" ]; then
    echo "OK: always-chain fired chain-to on non-clean exit (marker present)"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: always-chain set but B never ran (marker $MARK_AC missing)"
fi

# A should be STOPPED (it exited 1, restart=false). B should also have
# returned to STOPPED after its touch. Both being non-STARTED is the
# expected steady state.
assert_service_state "$SVC_A_AC" "STOPPED" "$SVC_A_AC parked STOPPED after exit 1"

svc_remove "$SVC_A_AC" "$SVC_B_AC"

# --- Sub-case B: control, no always-chain, A exits 1 → B does NOT fire ---
svc_deploy "$SVC_B_CTL" <<EOF
type = process
command = /bin/sh -c 'touch $MARK_CTL; exit 0'
restart = false
EOF

svc_deploy "$SVC_A_CTL" <<EOF
type = process
chain-to = $SVC_B_CTL
command = /bin/sh -c 'exit 1'
restart = false
EOF

slinitctl --system --no-wait start "$SVC_A_CTL" >/dev/null 2>&1

# Wait for A to actually finish so the stopping handler had its chance
# at the chain decision. Without this we could be asserting "no marker"
# before the chain *would* have fired.
_e=0
while [ "$_e" -lt 8 ]; do
    case "$(svc_state "$SVC_A_CTL")" in STOPPED|"") break ;; esac
    sleep 1
    _e=$((_e + 1))
done
# Generous settle for any deferred chain pathway.
sleep 2

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -e "$MARK_CTL" ]; then
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: default chain-to fired on non-clean exit (marker $MARK_CTL present)"
else
    echo "OK: default chain-to skipped — non-clean exit gates the chain"
fi

test_summary
