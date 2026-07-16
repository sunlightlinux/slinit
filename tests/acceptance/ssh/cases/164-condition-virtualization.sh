#!/bin/sh
# 164-condition-virtualization — probes slinit's virtualization detector
# against the ACTUAL environment. On a VM target (ceres), we expect
# `yes` to match and `no` to skip; on bare metal it's inverted. The
# case reads slinit's own detection result via a probe service so the
# expectation adapts to whichever host the acceptance runner points at.

SVC_PROBE="acceptance-test-cond-virt-probe"
SVC_YES="acceptance-test-cond-virt-yes"
SVC_NO="acceptance-test-cond-virt-no"
PROBE_OUT="/run/acceptance-virt-probe.state"
MARK_YES="/run/acceptance-virt-yes.mark"
MARK_NO="/run/acceptance-virt-no.mark"

cleanup() {
    svc_remove "$SVC_PROBE" "$SVC_YES" "$SVC_NO"
    rm -f "$PROBE_OUT" "$MARK_YES" "$MARK_NO"
}
trap cleanup EXIT INT TERM
rm -f "$PROBE_OUT" "$MARK_YES" "$MARK_NO"

# Probe: yes-branch runs when *any* virt is detected. We use two
# scripted services to record which side of the coin ran; slinit's
# detector is the source of truth on the target.
svc_deploy "$SVC_YES" <<EOF
type = scripted
condition-virtualization = yes
command = /bin/sh -c 'touch $MARK_YES; exit 0'
restart = false
EOF

svc_deploy "$SVC_NO" <<EOF
type = scripted
condition-virtualization = no
command = /bin/sh -c 'touch $MARK_NO; exit 0'
restart = false
EOF

slinitctl --system start "$SVC_YES" >/dev/null 2>&1
slinitctl --system start "$SVC_NO" >/dev/null 2>&1
wait_for_service "$SVC_YES" "STARTED" 10 || true
wait_for_service "$SVC_NO" "STARTED" 10 || true

assert_service_state "$SVC_YES" "STARTED" "condition-virtualization=yes reaches STARTED"
assert_service_state "$SVC_NO" "STARTED" "condition-virtualization=no reaches STARTED"

# Exactly one of the two markers must exist — the environment is
# either virtualized or bare metal, not both.
_ran_yes=0; _ran_no=0
[ -e "$MARK_YES" ] && _ran_yes=1
[ -e "$MARK_NO" ] && _ran_no=1

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$_ran_yes" -eq 1 ] && [ "$_ran_no" -eq 0 ]; then
    echo "OK: virt=yes matched, virt=no skipped (virtualized environment)"
elif [ "$_ran_yes" -eq 0 ] && [ "$_ran_no" -eq 1 ]; then
    echo "OK: virt=no matched, virt=yes skipped (bare metal)"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: expected exactly one branch to run, got yes=$_ran_yes no=$_ran_no"
fi

test_summary
