#!/bin/sh
# 115-stopsig — `stopsig` sends a custom signal on stop instead of
# SIGTERM. We use SIGUSR1 and confirm the child's handler ran.

SVC="${ACCEPTANCE_NS_PREFIX}stopsig"
MARKER="/tmp/acceptance-stopsig-marker"

cleanup() {
    svc_remove "$SVC"
    rm -f "$MARKER"
}
trap cleanup EXIT INT TERM
cleanup

# Child installs a USR1 handler that writes a marker and exits 0.
svc_deploy "$SVC" <<EOF
type = process
command = /bin/sh -c 'trap "echo caught-usr1 > $MARKER; exit 0" USR1; while true; do sleep 60; done'
stopsig = SIGUSR1
restart = no
EOF

slinitctl --system start "$SVC" 2>/dev/null
wait_for_service "$SVC" STARTED 10
assert_eq "$(svc_state "$SVC")" "STARTED" "service reached STARTED"

slinitctl --system stop "$SVC" 2>/dev/null
sleep 2

assert_eq "$(cat "$MARKER" 2>/dev/null)" "caught-usr1" \
    "stopsig delivered SIGUSR1 (child's handler ran)"

assert_eq "$(svc_state "$SVC")" "STOPPED" "service reached STOPPED"

test_summary
