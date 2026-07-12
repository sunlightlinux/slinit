#!/bin/sh
# Test: shared-logger-lossy = yes drops queued lines under backpressure
# instead of blocking the producer. Case 40 covers the basic multiplex;
# this pins the lossy opt-in wire-up.

LOGGER="test-shloglossy-logger"
PROD="test-shloglossy-prod"
OUT=/tmp/functional-shloglossy.log

cat > "/etc/slinit.d/$PROD" <<EOF
type = process
command = /bin/sh -c 'for i in 1 2 3 4 5; do printf "LOSSY-%d\\\\n" \$\$i; sleep 0.05; done; exec sleep 60'
shared-logger = $LOGGER
shared-logger-lossy = yes
shared-logger-queue-size = 16
restart = false
EOF

# LOGGER depends-on PRODUCER so the mux is set up before the logger's
# stdin is wired; reverse order leaves logger stdin as /dev/null and
# cat reads EOF immediately.
cat > "/etc/slinit.d/$LOGGER" <<EOF
type = process
command = /bin/sh -c 'exec cat > $OUT'
depends-on: $PROD
restart = false
EOF

slinitctl --system start "$LOGGER" >/dev/null 2>&1

wait_for_service "$LOGGER" "STARTED" 10 || true
assert_service_state "$LOGGER" "STARTED" "logger STARTED with lossy shared-logger"

wait_for_service "$PROD" "STARTED" 10 || true
assert_service_state "$PROD" "STARTED" "producer STARTED"

sleep 2

assert_contains "$(cat "$OUT" 2>/dev/null)" "LOSSY-" \
    "lossy producer's output landed at logger sink"

test_summary
