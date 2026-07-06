#!/bin/sh
# 109-umask — `umask` applied to the child before exec.

SVC="${ACCEPTANCE_NS_PREFIX}umask"
OUT="/tmp/acceptance-umask-out"

cleanup() {
    svc_remove "$SVC"
    rm -f "$OUT"
}
trap cleanup EXIT INT TERM
cleanup

# umask 077 → new files land as 0600, new dirs as 0700.
svc_deploy "$SVC" <<EOF
type = process
command = /bin/sh -c 'touch $OUT; while true; do sleep 60; done'
umask = 077
restart = no
EOF

slinitctl --system start "$SVC" 2>/dev/null
wait_for_service "$SVC" STARTED 10
sleep 0.5

_perm=$(stat -c '%a' "$OUT" 2>/dev/null)
assert_eq "$_perm" "600" "umask 077 → file created with mode 0600"

test_summary
