#!/bin/sh
# Test: state-directory + state-directory-mode creates /var/lib/<svc>
# with the requested mode.

SVC="test-sdmode"
DIR="/var/lib/$SVC"

cat > "/etc/slinit.d/$SVC" <<EOF
type = process
state-directory = $SVC
state-directory-mode = 0750
command = /bin/sh -c 'while :; do sleep 60; done'
restart = no
EOF

slinitctl --system start "$SVC" 2>/dev/null
wait_for_service "$SVC" STARTED 10
assert_service_state "$SVC" "STARTED" "service reached STARTED"

_mode=$(stat -c '%a' "$DIR" 2>/dev/null)
assert_eq "$_mode" "750" "state-directory mode = 750"

test_summary
