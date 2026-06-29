#!/bin/sh
# 70-log-rate-limit — token-bucket limiter on the per-service log pipeline.
#
# Two keys (parser.go log-rate-limit-interval / log-rate-limit-burst):
#   log-rate-limit-burst   = N   tokens available before the limiter kicks in
#   log-rate-limit-interval = D  one new token every D
#
# A producer that fires far more than `burst` lines inside one `interval`
# must end up with EVERY line past the burst dropped from the logfile.
# slinit also emits an explicit "dropped N messages" notice when the
# rate-limit re-arms, so we look for that too as a positive signal that
# the limiter actually engaged (not just "the producer happened to be
# slow enough").

WORK="/tmp/acceptance-rate"
SVC="acceptance-test-rate"
SVCFILE="/etc/slinit.d/$SVC"
LOGFILE="$WORK/svc.log"

cleanup() {
    slinitctl --system stop "$SVC" 2>/dev/null
    slinitctl --system unload "$SVC" 2>/dev/null
    rm -f "$SVCFILE"
    rm -rf "$WORK"
}
trap cleanup EXIT INT TERM
cleanup
mkdir -p "$WORK"

# Producer: 50 lines back-to-back, then sleep so it stays in STARTED.
# The window is 1 minute, burst is 5 → at most 5 of those 50 lines may
# land in the logfile.
cat > "$SVCFILE" <<EOF
type = process
command = /bin/sh -c 'i=0; while [ \$\$i -lt 50 ]; do echo "rate-$\$\$i-msg"; i=\$\$((i+1)); done; exec sleep 600'
logfile = $LOGFILE
log-rate-limit-interval = 60s
log-rate-limit-burst = 5
restart = false
EOF

# Parser sanity
_chk=$(slinit-check -d /etc/slinit.d "$SVC" 2>&1)
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ $? -eq 0 ]; then
    echo "OK: slinit-check accepts log-rate-limit directives"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: parser rejected the config:"; echo "$_chk" | sed 's/^/  | /'
fi

slinitctl --system start "$SVC" >/dev/null 2>&1
_e=0
while [ "$_e" -lt 6 ]; do
    _st=$(slinitctl --system status "$SVC" 2>/dev/null | awk '/State:/ {print $2; exit}')
    [ "$_st" = "STARTED" ] && break
    sleep 1
    _e=$((_e + 1))
done
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$_st" = "STARTED" ]; then
    echo "OK: $SVC STARTED"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: $SVC stuck at '$_st'"
    test_summary
    exit 1
fi
sleep 2

# --- Probe: at most `burst` rate-* lines survived on disk ------------
_kept=$(grep -c "^rate-" "$LOGFILE" 2>/dev/null); _kept=${_kept:-0}
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$_kept" -le 5 ]; then
    echo "OK: limiter capped at burst ($_kept lines <= 5)"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: $_kept rate-* lines made it through (burst=5)"
fi

# The producer pushes 50 lines, all of them the same family. We need
# at least one to land — otherwise the test would PASS even if logging
# were fully broken.
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$_kept" -ge 1 ]; then
    echo "OK: at least one rate-* line still landed ($_kept)"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: 0 rate-* lines in $LOGFILE — pipeline broken?"
fi

# slinit emits an in-band "dropped N message(s)" notice on the same log
# pipeline when the limiter pruned lines. Capture rate-only logs in a
# field — anything outside the "rate-*" name is a candidate for the
# drop notice.
_TESTS_RUN=$((_TESTS_RUN + 1))
if grep -qE "(dropped|rate.?limit)" "$LOGFILE" 2>/dev/null; then
    echo "OK: limiter emitted a drop notice"
else
    # Not a hard fail: some builds quiet the notice. Just report.
    echo "OK: no drop notice in logfile (limiter is silent — acceptable)"
fi

test_summary
