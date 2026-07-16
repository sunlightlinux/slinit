#!/bin/sh
# 167-condition-fraction — deterministic rollout gate. Bucket math is
# FNV(machine-id + tag) % 100 vs the requested percent, so 0% never
# picks anyone and 100% always picks everyone regardless of tag or
# machine-id. Requires /etc/machine-id (empty file also OK, since the
# hash is over the raw bytes).

SVC_ALL="acceptance-test-cond-frac-all"
SVC_NONE="acceptance-test-cond-frac-none"
MARK_ALL="/run/acceptance-cond-frac-all.mark"
MARK_NONE="/run/acceptance-cond-frac-none.mark"

cleanup() {
    svc_remove "$SVC_ALL" "$SVC_NONE"
    rm -f "$MARK_ALL" "$MARK_NONE"
}
trap cleanup EXIT INT TERM
rm -f "$MARK_ALL" "$MARK_NONE"

if [ ! -e /etc/machine-id ]; then
    _TESTS_RUN=$((_TESTS_RUN + 1))
    echo "SKIP: /etc/machine-id missing; fraction predicate untestable"
    test_summary
    exit 0
fi

svc_deploy "$SVC_ALL" <<EOF
type = scripted
condition-fraction = acceptance167:100
command = /bin/sh -c 'touch $MARK_ALL; exit 0'
restart = false
EOF

svc_deploy "$SVC_NONE" <<EOF
type = scripted
condition-fraction = acceptance167:0
command = /bin/sh -c 'touch $MARK_NONE; exit 0'
restart = false
EOF

slinitctl --system start "$SVC_ALL" >/dev/null 2>&1
slinitctl --system start "$SVC_NONE" >/dev/null 2>&1
wait_for_service "$SVC_ALL" "STARTED" 10 || true
wait_for_service "$SVC_NONE" "STARTED" 10 || true

assert_service_state "$SVC_ALL" "STARTED" "100% fraction reaches STARTED"
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -e "$MARK_ALL" ]; then
    echo "OK: 100% bucket ran command (universal match)"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: 100% bucket did not run"
fi

assert_service_state "$SVC_NONE" "STARTED" "0% fraction reaches STARTED"
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ ! -e "$MARK_NONE" ]; then
    echo "OK: 0% bucket skipped command (universal miss)"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: 0% bucket ran command anyway"
fi

test_summary
