#!/bin/sh
# 07-dependency — verify depends-on pulls a dependency up at start time and
# releases it on stop when no other consumer is left.
#
# Semantics: `slinitctl start LEAF` marks LEAF active; BASE is pulled up as a
# dependency but stays *unmarked*. Stopping LEAF therefore releases BASE
# automatically (no other consumer, no active mark).

BASE="acceptance-test-dep-base"
LEAF="acceptance-test-dep-leaf"

trap 'svc_remove "$LEAF" "$BASE"' EXIT INT TERM

svc_deploy "$BASE" <<EOF
type = process
command = /bin/sh -c 'while :; do sleep 60; done'
restart = false
EOF

svc_deploy "$LEAF" <<EOF
type = process
command = /bin/sh -c 'while :; do sleep 60; done'
depends-on: $BASE
restart = false
EOF

# Pull-up on start.
slinitctl --system start "$LEAF" >/dev/null 2>&1
wait_for_service "$LEAF" "STARTED" 10 || true
assert_service_state "$BASE" "STARTED" "$BASE pulled up by $LEAF"
assert_service_state "$LEAF" "STARTED" "$LEAF STARTED"

# Release on stop: stopping LEAF (which had the active mark) leaves BASE
# without any consumer or active mark — slinit must auto-stop it.
slinitctl --system stop "$LEAF" >/dev/null 2>&1
wait_for_service "$LEAF" "STOPPED" 10 || true
wait_for_service "$BASE" "STOPPED" 10 || true
assert_service_state "$LEAF" "STOPPED" "$LEAF STOPPED"
assert_service_state "$BASE" "STOPPED" "$BASE auto-released"

test_summary
