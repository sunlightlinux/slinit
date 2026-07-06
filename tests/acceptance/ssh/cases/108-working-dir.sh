#!/bin/sh
# 108-working-dir — `working-dir` sets the child's cwd before exec.

SVC="${ACCEPTANCE_NS_PREFIX}wdir"
WD="/tmp/acceptance-workingdir"
MARKER="/tmp/acceptance-wdir-out"

cleanup() {
    svc_remove "$SVC"
    rm -rf "$WD" "$MARKER"
}
trap cleanup EXIT INT TERM
cleanup
mkdir -p "$WD"

svc_deploy "$SVC" <<EOF
type = process
command = /bin/sh -c 'pwd > $MARKER; while true; do sleep 60; done'
working-dir = $WD
restart = no
EOF

slinitctl --system start "$SVC" 2>/dev/null
wait_for_service "$SVC" STARTED 10
sleep 0.5

assert_eq "$(cat "$MARKER" 2>/dev/null)" "$WD" "child's cwd matches working-dir"

test_summary
