#!/bin/sh
# 64-shared-logger — multi-producer → single logger pipe with [name] prefixing.
#
# slinit's SharedLogMux multiplexes the stdout of N producer services into
# the stdin of one logger service. Each forwarded line is prefixed with
# the producer's service name so the logger can demux on read. Wiring:
#
#   producer.conf:  shared-logger = my-logger
#   logger.conf:    type = process   (any process; it just consumes stdin)
#
# Order matters: a producer's BringUp registers a mux entry the moment it
# starts, so the logger's BringUp can attach via GetSharedLogMux(name).
# Make the logger depend on the producers (depends-on) to guarantee the
# mux exists by the time the logger's stdin is wired up.

LOGGER="acceptance-test-shlog-logger"
PROD1="acceptance-test-shlog-prod-1"
PROD2="acceptance-test-shlog-prod-2"
OUT="/tmp/acceptance-shlog.out"

cleanup() {
    for s in "$LOGGER" "$PROD1" "$PROD2"; do
        slinitctl --system stop "$s" 2>/dev/null
        slinitctl --system unload "$s" 2>/dev/null
        rm -f "/etc/slinit.d/$s"
    done
    rm -f "$OUT"
}
trap cleanup EXIT INT TERM
cleanup

# Note on escapes: slinit treats backslash as a parser escape (\n → "n",
# \\ → "\"). printf needs a real newline in its format string to flush
# one line at a time, so the format ends with \\\\n in the heredoc to
# yield "\\n" in the config file → "\n" after slinit's parse → newline
# in the shell-evaluated format string. echo (which appends a newline
# by default) is the cheaper alternative but loses %d formatting.
cat > "/etc/slinit.d/$PROD1" <<EOF
type = process
command = /bin/sh -c 'for n in 0 1 2 3 4; do printf "msg-from-prod-1 #%d\\\\n" \$\$n; sleep 1; done; exec sleep 60'
shared-logger = $LOGGER
restart = false
EOF

cat > "/etc/slinit.d/$PROD2" <<EOF
type = process
command = /bin/sh -c 'for n in 0 1 2 3 4; do printf "msg-from-prod-2 #%d\\\\n" \$\$n; sleep 1; done; exec sleep 60'
shared-logger = $LOGGER
restart = false
EOF

# Logger: cat its stdin to a file. \$\$ literal so cat sees the runtime
# pipe, not a config-parse-time expansion.
cat > "/etc/slinit.d/$LOGGER" <<EOF
type = process
command = /bin/sh -c 'exec cat > $OUT'
depends-on: $PROD1
depends-on: $PROD2
restart = false
EOF

# Start the logger; depends-on pulls producers up first, which creates
# the mux so the logger's stdin can attach to the read-end.
slinitctl --system start "$LOGGER" >/dev/null 2>&1

_e=0
while [ "$_e" -lt 10 ]; do
    _st=$(slinitctl --system status "$LOGGER" 2>/dev/null | awk '/State:/ {print $2; exit}')
    [ "$_st" = "STARTED" ] && break
    sleep 1
    _e=$((_e + 1))
done

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$_st" = "STARTED" ]; then
    echo "OK: logger $LOGGER STARTED (deps pulled producers up first)"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: logger stuck at '$_st'"
    test_summary
    exit 1
fi

# Verify both producers also landed in STARTED.
for p in "$PROD1" "$PROD2"; do
    _pst=$(slinitctl --system status "$p" 2>/dev/null | awk '/State:/ {print $2; exit}')
    _TESTS_RUN=$((_TESTS_RUN + 1))
    if [ "$_pst" = "STARTED" ]; then
        echo "OK: $p STARTED"
    else
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: $p stuck at '$_pst'"
    fi
done

# Producers emit one line per second for 5 seconds. Wait long enough to
# collect at least a couple of lines from each side of the mux.
sleep 6

# --- Probe: $OUT has prefixed lines from both producers --------------
_TESTS_RUN=$((_TESTS_RUN + 1))
if grep -q "^\[$PROD1\] msg-from-prod-1" "$OUT" 2>/dev/null; then
    echo "OK: lines from $PROD1 are prefixed with [$PROD1]"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: no [$PROD1] prefix in $OUT"
    head -10 "$OUT" 2>/dev/null | sed 's/^/  | /'
fi

_TESTS_RUN=$((_TESTS_RUN + 1))
if grep -q "^\[$PROD2\] msg-from-prod-2" "$OUT" 2>/dev/null; then
    echo "OK: lines from $PROD2 are prefixed with [$PROD2]"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: no [$PROD2] prefix in $OUT"
    head -10 "$OUT" 2>/dev/null | sed 's/^/  | /'
fi

# --- Probe: at least 2 distinct messages from each ------------------
_c1=$(grep -c "^\[$PROD1\] " "$OUT" 2>/dev/null); _c1=${_c1:-0}
_c2=$(grep -c "^\[$PROD2\] " "$OUT" 2>/dev/null); _c2=${_c2:-0}
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$_c1" -ge 2 ] && [ "$_c2" -ge 2 ]; then
    echo "OK: mux drained both producers ($PROD1: $_c1 lines, $PROD2: $_c2 lines)"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: mux short-changed a producer (got $PROD1=$_c1, $PROD2=$_c2; expected each >=2)"
    cat "$OUT" 2>/dev/null | sed 's/^/  | /'
fi

# --- Probe: no untagged lines (the logger should only see [name] entries) -
_untagged=$(grep -cv '^\[' "$OUT" 2>/dev/null); _untagged=${_untagged:-0}
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$_untagged" -eq 0 ]; then
    echo "OK: no untagged lines (every entry carries a producer prefix)"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: $_untagged untagged lines leaked through:"
    grep -v '^\[' "$OUT" 2>/dev/null | head -5 | sed 's/^/  | /'
fi

test_summary
