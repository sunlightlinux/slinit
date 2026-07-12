#!/bin/sh
# Test: bundle-of = svc1, svc2, svc3 desugars to type=internal +
# depends-on for each member. Starting the bundle pulls up every
# member; stopping the bundle propagates a stop; `slinitctl status`
# renders a "Bundle members:" section with each member's live state.

MEMBER_A="test-bundle-a"
MEMBER_B="test-bundle-b"
BUNDLE="test-bundle-grp"

cat > "/etc/slinit.d/$MEMBER_A" <<EOF
type = process
command = /bin/sh -c 'while :; do sleep 60; done'
restart = no
EOF
cat > "/etc/slinit.d/$MEMBER_B" <<EOF
type = process
command = /bin/sh -c 'while :; do sleep 60; done'
restart = no
EOF
cat > "/etc/slinit.d/$BUNDLE" <<EOF
bundle-of = $MEMBER_A, $MEMBER_B
EOF

slinitctl --system start "$BUNDLE" >/dev/null 2>&1
wait_for_service "$BUNDLE" "STARTED" 10 || true
assert_service_state "$BUNDLE" "STARTED" "bundle STARTED"
wait_for_service "$MEMBER_A" "STARTED" 10 || true
assert_service_state "$MEMBER_A" "STARTED" "$MEMBER_A pulled up by bundle"
wait_for_service "$MEMBER_B" "STARTED" 10 || true
assert_service_state "$MEMBER_B" "STARTED" "$MEMBER_B pulled up by bundle"

# status output must include the bundle-members section.
_status=$(slinitctl --system status "$BUNDLE" 2>&1)
assert_contains "$_status" "Bundle members:" \
    "status renders 'Bundle members:' header"
assert_contains "$_status" "$MEMBER_A" \
    "status lists $MEMBER_A under the bundle"
assert_contains "$_status" "$MEMBER_B" \
    "status lists $MEMBER_B under the bundle"

# Stopping the bundle should cascade STOP through the depends-on chain.
slinitctl --system stop "$BUNDLE" >/dev/null 2>&1
wait_for_service "$BUNDLE" "STOPPED" 10 || true
assert_service_state "$BUNDLE" "STOPPED" "bundle STOPPED after stop"
wait_for_service "$MEMBER_A" "STOPPED" 10 || true
assert_service_state "$MEMBER_A" "STOPPED" "$MEMBER_A stopped by bundle cascade"
wait_for_service "$MEMBER_B" "STOPPED" 10 || true
assert_service_state "$MEMBER_B" "STOPPED" "$MEMBER_B stopped by bundle cascade"

# Non-bundle service must NOT surface a Bundle members: section.
_status=$(slinitctl --system status "$MEMBER_A" 2>&1)
assert_not_contains "$_status" "Bundle members:" \
    "non-bundle status stays quiet about members"

test_summary
