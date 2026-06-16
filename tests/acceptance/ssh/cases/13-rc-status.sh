#!/bin/sh
# 13-rc-status — rc-status lists every loaded service; a freshly started
# one of ours must show up with the "+" running mark.

SVC="acceptance-test-rc-status"

trap 'svc_remove "$SVC"' EXIT INT TERM

svc_deploy "$SVC" <<EOF
type = process
command = /bin/sh -c 'while :; do sleep 60; done'
restart = false
EOF

slinitctl --system start "$SVC" >/dev/null 2>&1
wait_for_service "$SVC" "STARTED" 10 || true

_out="$(rc-status 2>&1)"
assert_contains "$_out" "$SVC" "rc-status mentions $SVC"

# slinit's list marker for a running service is '{+}' (process up) or
# '[+]' (milestone up). Either is acceptable; both contain '+'.
_svc_line="$(echo "$_out" | grep -- "$SVC")"
assert_contains "$_svc_line" "+" "$SVC line carries '+' running marker"

# Essential services should also be in rc-status (sanity).
assert_contains "$_out" "sshd" "rc-status lists sshd"

test_summary
