#!/bin/sh
# 72-output-error-logger — OpenRC OUTPUT_LOGGER / ERROR_LOGGER plumbing.
#
# Two adjacent process options that spawn external commands and pipe
# child stdout / stderr into them. With both set, stdout and stderr
# travel through DIFFERENT pipelines, so a downstream syslog/journal
# integration can tag them differently. With only output-logger set,
# stderr is merged into stdout's pipe.
#
# Each child process gets one logger pair; we verify both halves are
# fed by checking two sinks the helper scripts append to.

WORK="/tmp/acceptance-oel"
SVC="acceptance-test-oel"
SVCFILE="/etc/slinit.d/$SVC"
OUT="$WORK/stdout.log"
ERR="$WORK/stderr.log"
OUT_HOOK="$WORK/out.sh"
ERR_HOOK="$WORK/err.sh"

cleanup() {
    slinitctl --system stop "$SVC" 2>/dev/null
    slinitctl --system unload "$SVC" 2>/dev/null
    rm -f "$SVCFILE"
    rm -rf "$WORK"
}
trap cleanup EXIT INT TERM
cleanup
mkdir -p "$WORK"

# Each hook prefixes whatever it reads on stdin with a tag so we can
# tell the two streams apart at assert time. sed/cat would buffer until
# EOF (the child keeps stdout/stderr open via `sleep 600`), so use a
# line-by-line read loop that appends each line as it arrives.
cat > "$OUT_HOOK" <<EOF
#!/bin/sh
while IFS= read -r line; do echo "STDOUT:\$line" >> $OUT; done
EOF
cat > "$ERR_HOOK" <<EOF
#!/bin/sh
while IFS= read -r line; do echo "STDERR:\$line" >> $ERR; done
EOF
chmod +x "$OUT_HOOK" "$ERR_HOOK"

cat > "$SVCFILE" <<EOF
type = process
command = /bin/sh -c 'echo "from-stdout-line"; echo "from-stderr-line" >&2; exec sleep 600'
log-type = command
output-logger = $OUT_HOOK
error-logger = $ERR_HOOK
restart = false
EOF

_chk=$(slinit-check -d /etc/slinit.d "$SVC" 2>&1)
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ $? -eq 0 ]; then
    echo "OK: slinit-check accepts output-logger + error-logger"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: parser rejected:"; echo "$_chk" | sed 's/^/  | /'
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
    echo "OK: $SVC STARTED with both loggers wired"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: $SVC stuck at '$_st'"
    test_summary
    exit 1
fi

# Give the loggers a moment to flush their pipes.
sleep 1

# --- Probe: stdout sink got the stdout line, NOT the stderr line -----
_TESTS_RUN=$((_TESTS_RUN + 1))
if grep -q "^STDOUT:from-stdout-line$" "$OUT" 2>/dev/null; then
    echo "OK: output-logger received the child's stdout"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: stdout sink missing the expected line:"
    sed 's/^/  | /' "$OUT" 2>/dev/null
fi

_TESTS_RUN=$((_TESTS_RUN + 1))
if ! grep -q "from-stderr-line" "$OUT" 2>/dev/null; then
    echo "OK: stdout sink isolated from stderr"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: stdout sink leaked stderr content"
fi

# --- Probe: stderr sink got the stderr line, NOT the stdout line -----
_TESTS_RUN=$((_TESTS_RUN + 1))
if grep -q "^STDERR:from-stderr-line$" "$ERR" 2>/dev/null; then
    echo "OK: error-logger received the child's stderr"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: stderr sink missing the expected line:"
    sed 's/^/  | /' "$ERR" 2>/dev/null
fi

_TESTS_RUN=$((_TESTS_RUN + 1))
if ! grep -q "from-stdout-line" "$ERR" 2>/dev/null; then
    echo "OK: stderr sink isolated from stdout"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: stderr sink leaked stdout content"
fi

test_summary
