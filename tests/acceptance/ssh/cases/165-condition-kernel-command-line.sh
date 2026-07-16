#!/bin/sh
# 165-condition-kernel-command-line — matches whole-word tokens in
# /proc/cmdline (key alone OR key=value exact match). We drive both
# positive and negative from tokens picked at test time: the first
# real token off /proc/cmdline is guaranteed to be present; a
# random-looking token below is guaranteed to be absent.

SVC_HIT="acceptance-test-cond-cmdline-hit"
SVC_MISS="acceptance-test-cond-cmdline-miss"
MARK_HIT="/run/acceptance-cond-cmdline-hit.mark"
MARK_MISS="/run/acceptance-cond-cmdline-miss.mark"
# Long enough it will not clash with any real kernel arg on any target.
MISS_TOKEN="slinit-acceptance-165-doesnotexist"

cleanup() {
    svc_remove "$SVC_HIT" "$SVC_MISS"
    rm -f "$MARK_HIT" "$MARK_MISS"
}
trap cleanup EXIT INT TERM
rm -f "$MARK_HIT" "$MARK_MISS"

HIT_TOKEN=$(awk '{print $1; exit}' /proc/cmdline)
if [ -z "$HIT_TOKEN" ]; then
    _TESTS_RUN=$((_TESTS_RUN + 1))
    echo "SKIP: /proc/cmdline empty — no token to match"
    test_summary
    exit 0
fi

svc_deploy "$SVC_HIT" <<EOF
type = scripted
condition-kernel-command-line = $HIT_TOKEN
command = /bin/sh -c 'touch $MARK_HIT; exit 0'
restart = false
EOF

svc_deploy "$SVC_MISS" <<EOF
type = scripted
condition-kernel-command-line = $MISS_TOKEN
command = /bin/sh -c 'touch $MARK_MISS; exit 0'
restart = false
EOF

slinitctl --system start "$SVC_HIT" >/dev/null 2>&1
slinitctl --system start "$SVC_MISS" >/dev/null 2>&1
wait_for_service "$SVC_HIT" "STARTED" 10 || true
wait_for_service "$SVC_MISS" "STARTED" 10 || true

assert_service_state "$SVC_HIT" "STARTED" "hit-token reaches STARTED"
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -e "$MARK_HIT" ]; then
    echo "OK: matched '$HIT_TOKEN' in /proc/cmdline"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: expected match for '$HIT_TOKEN' but command skipped"
fi

assert_service_state "$SVC_MISS" "STARTED" "miss-token still reaches STARTED"
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ ! -e "$MARK_MISS" ]; then
    echo "OK: '$MISS_TOKEN' not in cmdline → command skipped"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: bogus token unexpectedly matched"
fi

test_summary
