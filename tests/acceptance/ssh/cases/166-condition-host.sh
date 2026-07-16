#!/bin/sh
# 166-condition-host — case-insensitive whole-hostname match against
# `hostname` on the target. Drive with the real hostname (must match)
# and a randomised sibling (must skip).

SVC_HIT="acceptance-test-cond-host-hit"
SVC_MISS="acceptance-test-cond-host-miss"
MARK_HIT="/run/acceptance-cond-host-hit.mark"
MARK_MISS="/run/acceptance-cond-host-miss.mark"

cleanup() {
    svc_remove "$SVC_HIT" "$SVC_MISS"
    rm -f "$MARK_HIT" "$MARK_MISS"
}
trap cleanup EXIT INT TERM
rm -f "$MARK_HIT" "$MARK_MISS"

MY_HOST=$(hostname)
if [ -z "$MY_HOST" ]; then
    _TESTS_RUN=$((_TESTS_RUN + 1))
    echo "SKIP: hostname empty on this target"
    test_summary
    exit 0
fi
# case-insensitive match should still succeed — upper-case the hostname.
UP_HOST=$(printf '%s' "$MY_HOST" | tr '[:lower:]' '[:upper:]')

svc_deploy "$SVC_HIT" <<EOF
type = scripted
condition-host = $UP_HOST
command = /bin/sh -c 'touch $MARK_HIT; exit 0'
restart = false
EOF

svc_deploy "$SVC_MISS" <<EOF
type = scripted
condition-host = ${MY_HOST}-not-this-name
command = /bin/sh -c 'touch $MARK_MISS; exit 0'
restart = false
EOF

slinitctl --system start "$SVC_HIT" >/dev/null 2>&1
slinitctl --system start "$SVC_MISS" >/dev/null 2>&1
wait_for_service "$SVC_HIT" "STARTED" 10 || true
wait_for_service "$SVC_MISS" "STARTED" 10 || true

assert_service_state "$SVC_HIT" "STARTED" "matched host reaches STARTED"
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -e "$MARK_HIT" ]; then
    echo "OK: hostname match ('$UP_HOST' == '$MY_HOST' case-insensitively)"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: case-insensitive hostname match did not run"
fi

assert_service_state "$SVC_MISS" "STARTED" "mismatched host still reaches STARTED"
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ ! -e "$MARK_MISS" ]; then
    echo "OK: mismatched hostname skipped command"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: mismatched hostname ran command"
fi

test_summary
