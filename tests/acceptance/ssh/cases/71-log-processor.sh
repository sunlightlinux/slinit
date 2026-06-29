#!/bin/sh
# 71-log-processor — external command runs on every rotated log file.
#
# pkg/service/logrotate.go runProcessor invokes
#   <log-processor cmd> <rotated-file>
# after each rotation. We park a tiny script that appends the rotated
# file's basename to a sink, force enough output to rotate at least
# once, and confirm the sink picked up the rotated name.

WORK="/tmp/acceptance-logproc"
SVC="acceptance-test-logproc"
SVCFILE="/etc/slinit.d/$SVC"
LOGFILE="$WORK/svc.log"
PROC="$WORK/process.sh"
PROCLOG="$WORK/processed"

cleanup() {
    slinitctl --system stop "$SVC" 2>/dev/null
    slinitctl --system unload "$SVC" 2>/dev/null
    rm -f "$SVCFILE"
    rm -rf "$WORK"
}
trap cleanup EXIT INT TERM
cleanup
mkdir -p "$WORK"

# Processor: log the rotated path. Touch a marker so we can also confirm
# the script was invoked at least once (cheap presence check).
cat > "$PROC" <<EOF
#!/bin/sh
printf 'rotated:%s\n' "\$1" >> $PROCLOG
EOF
chmod +x "$PROC"

cat > "$SVCFILE" <<EOF
type = process
command = /bin/sh -c 'i=0; while [ \$\$i -lt 80 ]; do printf "line-%d-padding-padding-padding-padding\\\\n" \$\$i; i=\$\$((i+1)); done; exec sleep 600'
logfile = $LOGFILE
logfile-max-size = 300
logfile-max-files = 5
log-processor = $PROC
restart = false
EOF

_chk=$(slinit-check -d /etc/slinit.d "$SVC" 2>&1)
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ $? -eq 0 ]; then
    echo "OK: slinit-check accepts log-processor directive"
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

# Give the rotator time to fire + processor to run.
sleep 3

# --- Probe: rotation happened ---------------------------------------
_rot=$(ls "$WORK" 2>/dev/null | grep -c "^svc\.log\.")
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$_rot" -ge 1 ]; then
    echo "OK: rotation produced $_rot rotated files"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: no rotated files in $WORK:"
    ls -la "$WORK" 2>/dev/null | sed 's/^/  | /'
fi

# --- Probe: processor was invoked at least once ---------------------
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -f "$PROCLOG" ] && [ -s "$PROCLOG" ]; then
    _n=$(wc -l < "$PROCLOG"); _n=${_n:-0}
    echo "OK: log-processor invoked $_n time(s)"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: $PROCLOG missing or empty — processor never ran"
fi

# --- Probe: processor received the rotated file path ----------------
_TESTS_RUN=$((_TESTS_RUN + 1))
if grep -q "rotated:.*svc\\.log" "$PROCLOG" 2>/dev/null; then
    echo "OK: processor received a path under $WORK"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: processor arg didn't contain a svc.log path; got:"
    sed 's/^/  | /' "$PROCLOG" 2>/dev/null
fi

test_summary
