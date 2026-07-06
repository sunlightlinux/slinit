#!/bin/sh
# 139-state-directory-mode — /var/lib/<svc> gets the requested mode.

SVC="${ACCEPTANCE_NS_PREFIX}sdmode"
DIR="/var/lib/$SVC"

cleanup() {
    svc_remove "$SVC"
    rm -rf "$DIR"
}
trap cleanup EXIT INT TERM
cleanup

svc_deploy "$SVC" <<EOF
type = process
state-directory = $SVC
state-directory-mode = 0750
command = /bin/sh -c 'while :; do sleep 60; done'
restart = no
EOF

slinitctl --system start "$SVC" 2>/dev/null
wait_for_service "$SVC" STARTED 10
assert_eq "$(svc_state "$SVC")" "STARTED" "service reached STARTED"

_mode=$(stat -c '%a' "$DIR" 2>/dev/null)
assert_eq "$_mode" "750" "state-directory mode = 750"

test_summary
