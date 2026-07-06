#!/bin/sh
# 141-runtime-directory-preserve — with `= yes`, /run/<svc> and its
# contents survive after the service stops.

SVC="${ACCEPTANCE_NS_PREFIX}rdpres"
DIR="/run/$SVC"

cleanup() {
    svc_remove "$SVC"
    rm -rf "$DIR"
}
trap cleanup EXIT INT TERM
cleanup

svc_deploy "$SVC" <<EOF
type = process
runtime-directory = $SVC
runtime-directory-preserve = yes
command = /bin/sh -c 'touch $DIR/marker; while :; do sleep 60; done'
restart = no
EOF

slinitctl --system start "$SVC" 2>/dev/null
wait_for_service "$SVC" STARTED 10
assert_eq "$(svc_state "$SVC")" "STARTED" "service reached STARTED"
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
