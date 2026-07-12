#!/bin/sh
# 155-shared-logger-lossy — shared-logger-lossy = yes drops queued
# lines under backpressure instead of blocking the producer. Case 64
# already covers the basic multiplex; this case pins the lossy opt-in
# wire-up (config surface + producer output still reaches the sink
# under the lossy path).
#
# Wiring mirrors case 64: the LOGGER depends-on the PRODUCER so slinit
# brings up the producer first, its BringUp registers the shared-log
# mux entry, then the logger's BringUp attaches to the read-end via
# GetSharedLogMux(name). Reversing that order leaves the logger's stdin
# as /dev/null and `cat` reads EOF immediately, exiting.

LOGGER="acceptance-test-shloglossy-logger"
PROD="acceptance-test-shloglossy-prod"
OUT=/tmp/acceptance-shloglossy.$$.log

cleanup() {
    # Order matters: LOGGER holds a shared-logger mux reference to
    # PROD, so unloading PROD first can leave the mux entry pinned and
    # slinit refuses to drop PROD from the loaded set (case 999 then
    # spots the leak). Case 64-shared-logger uses the same LOGGER-first
    # ordering.
    svc_remove "$LOGGER" "$PROD"
    rm -f "$OUT"
}
trap cleanup EXIT INT TERM

# Producer: emit 5 quick tagged lines then park. Backslash-quadrupled
# `\\\\n` so slinit's parse yields `\n` in the sh printf format string
# (see case 64 for the two-step escape rule).
cat > "/etc/slinit.d/$PROD" <<EOF
type = process
command = /bin/sh -c 'for i in 1 2 3 4 5; do printf "LOSSY-%d\\\\n" \$\$i; sleep 0.05; done; exec sleep 60'
shared-logger = $LOGGER
shared-logger-lossy = yes
shared-logger-queue-size = 16
restart = false
EOF

# Logger: cat producer stream to file. depends-on producer so the mux
# is set up before the logger's stdin is wired.
cat > "/etc/slinit.d/$LOGGER" <<EOF
type = process
command = /bin/sh -c 'exec cat > $OUT'
depends-on: $PROD
restart = false
EOF

# Starting the logger transitively pulls the producer up first.
slinitctl --system start "$LOGGER" >/dev/null 2>&1

wait_for_service "$LOGGER" "STARTED" 10 || true
assert_service_state "$LOGGER" "STARTED" "logger STARTED with lossy shared-logger"

wait_for_service "$PROD" "STARTED" 10 || true
assert_service_state "$PROD" "STARTED" "producer STARTED"

# Give the mux time to drain.
sleep 2

# Expect at least one LOSSY-N line to have reached the logger sink.
# (Any subset is acceptable — the whole point of lossy mode is that
# drops are allowed.)
assert_contains "$(cat "$OUT" 2>/dev/null)" "LOSSY-" \
    "lossy producer's output landed at logger sink"

test_summary
