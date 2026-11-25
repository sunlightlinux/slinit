#!/bin/sh
# Test: shared-logger-lossy = yes drops lines under backpressure instead
# of blocking the producer. Case 40 covers the basic multiplex.
#
# The logger does not read its stdin for its first 4 s. The producer
# writes 4000 lines (about 240 KB, more than the two pipe buffers in
# between) and then touches a marker. Lossy, the mux drops what the
# 16-line queue cannot hold and the producer finishes at once; without
# lossy it blocks until the logger starts reading. Once the logger
# reads, it must have received some lines but not all of them. (The
# case used to check only that a few lines arrived, which they do
# either way.)

LOGGER="test-shloglossy-logger"
PROD="test-shloglossy-prod"
OUT=/tmp/functional-shloglossy.log
DONE=/run/103-prod-done

cat > "/etc/slinit.d/$PROD" <<'EOF2'
type = process
command = /bin/sh -c "i=0; while [ $$i -lt 4000 ]; do echo LOSSY-$$i-padding-padding-padding-padding-padding; i=$$((i + 1)); done; touch /run/103-prod-done; exec sleep 60"
shared-logger = test-shloglossy-logger
restart = false
EOF2

# LOGGER depends-on PRODUCER so the mux is set up before the logger's
# stdin is wired; reverse order leaves logger stdin as /dev/null and
# cat reads EOF immediately. The drop policy belongs to the logger
# (slinit-service(5)); the case used to set it on the producer, where
# it does nothing.
cat > "/etc/slinit.d/$LOGGER" <<EOF2
type = process
command = /bin/sh -c 'sleep 4; exec cat > $OUT'
depends-on: $PROD
shared-logger-lossy = yes
shared-logger-queue-size = 16
restart = false
EOF2

slinitctl --system start "$LOGGER" >/dev/null 2>&1

wait_for_service "$LOGGER" "STARTED" 10 || true
assert_service_state "$LOGGER" "STARTED" "logger STARTED with lossy shared-logger"
assert_service_state "$PROD" "STARTED" "producer STARTED"

# Well inside the logger's 4 s of not reading.
sleep 1.5
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -e "$DONE" ]; then
    echo "OK: producer finished while the logger was not reading"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: producer is blocked behind the logger"
fi

# The logger starts reading 4 s in; give it time to drain.
sleep 4
got=$(grep -c LOSSY- "$OUT" 2>/dev/null)
got=${got:-0}
echo "INFO: logger received $got of 4000 lines"
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$got" -gt 0 ] && [ "$got" -lt 4000 ]; then
    echo "OK: some lines arrived and the rest were dropped"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: expected between 1 and 3999 lines, got $got"
fi

test_summary
