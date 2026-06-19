#!/bin/sh
# 29-predicates — `condition-*` skips the start silently when false
# (service moves to STARTED but no process runs); `assert-*` fails the
# start when false. Probe three cases:
#   (a) condition-path-exists = $PRESENT  → command runs (marker written)
#   (b) condition-path-exists = $ABSENT   → command does NOT run, but
#       service still reaches STARTED so dependents can proceed
#   (c) condition-path-exists = !$ABSENT  → negation passes, command runs

SVC_A="acceptance-test-cond-true"
SVC_B="acceptance-test-cond-false"
SVC_C="acceptance-test-cond-neg"
PRESENT="/run/acceptance-cond-present"
ABSENT="/run/acceptance-cond-absent-does-not-exist"
MARK_A="/run/acceptance-cond-a.mark"
MARK_B="/run/acceptance-cond-b.mark"
MARK_C="/run/acceptance-cond-c.mark"

cleanup() {
    svc_remove "$SVC_A" "$SVC_B" "$SVC_C"
    rm -f "$PRESENT" "$MARK_A" "$MARK_B" "$MARK_C"
}
trap cleanup EXIT INT TERM

# Make sure $PRESENT exists and $ABSENT does not.
touch "$PRESENT"
rm -f "$ABSENT" "$MARK_A" "$MARK_B" "$MARK_C"

svc_deploy "$SVC_A" <<EOF
type = scripted
condition-path-exists = $PRESENT
command = /bin/sh -c 'touch $MARK_A; exit 0'
restart = false
EOF

svc_deploy "$SVC_B" <<EOF
type = scripted
condition-path-exists = $ABSENT
command = /bin/sh -c 'touch $MARK_B; exit 0'
restart = false
EOF

svc_deploy "$SVC_C" <<EOF
type = scripted
condition-path-exists = !$ABSENT
command = /bin/sh -c 'touch $MARK_C; exit 0'
restart = false
EOF

slinitctl --system start "$SVC_A" >/dev/null 2>&1
slinitctl --system start "$SVC_B" >/dev/null 2>&1
slinitctl --system start "$SVC_C" >/dev/null 2>&1
wait_for_service "$SVC_A" "STARTED" 10 || true
wait_for_service "$SVC_B" "STARTED" 10 || true
wait_for_service "$SVC_C" "STARTED" 10 || true

# (a) condition true → command ran.
assert_service_state "$SVC_A" "STARTED" "true condition reaches STARTED"
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -e "$MARK_A" ]; then
    echo "OK: command ran when condition true"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: true-condition svc did not run its command"
fi

# (b) condition false → STARTED *and* command was skipped.
assert_service_state "$SVC_B" "STARTED" "false condition still reaches STARTED (skipped)"
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ ! -e "$MARK_B" ]; then
    echo "OK: command skipped when condition false"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: false-condition svc ran its command anyway"
fi

# (c) negated absent → command ran.
assert_service_state "$SVC_C" "STARTED" "negated condition reaches STARTED"
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -e "$MARK_C" ]; then
    echo "OK: command ran when negated condition true"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: negated-condition svc did not run its command"
fi

test_summary
