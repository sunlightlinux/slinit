#!/bin/sh
# Test: runtime-directory-preserve = yes keeps /run/<svc>/... after
# the service stops (default behaviour clears it).

SVC="test-rdpres"
DIR="/run/$SVC"

cat > "/etc/slinit.d/$SVC" <<EOF
type = process
runtime-directory = $SVC
runtime-directory-preserve = yes
command = /bin/sh -c 'touch $DIR/marker; while :; do sleep 60; done'
restart = no
EOF

slinitctl --system start "$SVC" 2>/dev/null
wait_for_service "$SVC" STARTED 10
assert_service_state "$SVC" "STARTED" "service reached STARTED"
sleep 0.3

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -f "$DIR/marker" ]; then
    echo "OK: $DIR/marker exists while running"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: marker missing while running"
fi

slinitctl --system stop "$SVC" 2>/dev/null
wait_for_service "$SVC" STOPPED 10

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -f "$DIR/marker" ]; then
    echo "OK: $DIR/marker preserved after stop"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: marker cleaned up despite preserve=yes"
fi

test_summary
