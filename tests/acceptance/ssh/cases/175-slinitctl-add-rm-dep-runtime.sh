#!/bin/sh
# 175-slinitctl-add-rm-dep-runtime — `add-dep` and `rm-dep` mutate the
# service dependency graph at runtime without a reload. Round-trip: add
# a waits-for edge, verify it shows up in dependents/status, remove it,
# verify it's gone.

SVC_A="acceptance-test-adddep-a"
SVC_B="acceptance-test-adddep-b"

cleanup() {
    slinitctl --system rm-dep "$SVC_A" waits-for "$SVC_B" >/dev/null 2>&1 || true
    svc_remove "$SVC_A" "$SVC_B"
}
trap cleanup EXIT INT TERM

svc_deploy "$SVC_A" <<EOF
type = process
command = /bin/sh -c 'exec sleep 600'
restart = false
EOF

svc_deploy "$SVC_B" <<EOF
type = process
command = /bin/sh -c 'exec sleep 600'
restart = false
EOF

slinitctl --system start "$SVC_A" >/dev/null
slinitctl --system start "$SVC_B" >/dev/null
wait_for_service "$SVC_A" "STARTED" 10 || { test_summary; return; }
wait_for_service "$SVC_B" "STARTED" 10 || { test_summary; return; }

# slinitctl add-dep syntax: <from> <dep-type> <to>.
# "A waits-for B" → add-dep A waits-for B.
slinitctl --system add-dep "$SVC_A" waits-for "$SVC_B" >/dev/null 2>&1
_rc=$?
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$_rc" -eq 0 ]; then
    echo "OK: add-dep '$SVC_A waits-for $SVC_B' accepted"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: add-dep exit $_rc"
fi

# dependents of B should now include A.
_dependents=$(slinitctl --system dependents "$SVC_B" 2>&1)
assert_contains "$_dependents" "$SVC_A" "$SVC_A appears as dependent of $SVC_B after add-dep"

# Remove the dep.
slinitctl --system rm-dep "$SVC_A" waits-for "$SVC_B" >/dev/null 2>&1
_dependents=$(slinitctl --system dependents "$SVC_B" 2>&1)
assert_not_contains "$_dependents" "$SVC_A" "$SVC_A no longer a dependent after rm-dep"

test_summary
