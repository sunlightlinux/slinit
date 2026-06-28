#!/bin/sh
# 66-log-rotation-filter — logfile-max-size rotation + log-include/exclude
# regex filtering on the per-service stdout pipeline.
#
# Config keys (pkg/config/parser.go):
#   logfile-max-size = <bytes>     rotate when current file grows past N bytes
#   logfile-max-files = <N>        keep at most N rotated copies (oldest gets
#                                  pruned)
#   log-include = <regex>          drop lines that don't match (additive)
#   log-exclude = <regex>          drop lines that DO match (additive)
#
# We feed the producer a known mix of "keep" and "noise" lines, force enough
# volume to rotate, then assert (a) rotated files exist, (b) the noise lines
# never landed, (c) the kept lines made it through.

WORK="/tmp/acceptance-logrot"
SVC="acceptance-test-logrot"
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

# Producer: emits ~100 lines alternating "KEEP-N" and "DROP-N". Each line is
# ~12 bytes; logfile-max-size=200 rotates after every ~17 kept lines, so
# we end up with at least one rotated file.
cat > "$SVCFILE" <<EOF
type = process
command = /bin/sh -c 'i=0; while [ \$\$i -lt 100 ]; do printf "KEEP-%d\\\\n" \$\$i; printf "DROP-%d\\\\n" \$\$i; i=\$\$((i+1)); done; exec sleep 600'
logfile = $LOGFILE
logfile-max-size = 200
logfile-max-files = 3
log-include = ^KEEP
log-exclude = ^DROP
restart = false
EOF

# --- Probe 1: slinit-check accepts the whole config -----------------
_chk=$(slinit-check -d /etc/slinit.d "$SVC" 2>&1)
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ $? -eq 0 ]; then
    echo "OK: slinit-check accepts the log-rotation/filter directives"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: slinit-check rejected the config:"; echo "$_chk" | sed 's/^/  | /'
fi

# --- Start service and wait for output to flush ---------------------
slinitctl --system start "$SVC" >/dev/null 2>&1
_e=0
while [ "$_e" -lt 8 ]; do
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

# --- Probe 2: rotation actually rotated -----------------------------
# At max-size 200 with ~17 lines per file, 100 KEEP-* lines force several
# rotations; max-files caps at 3 (current + up to 3 rotated copies).
_rot=$(ls "$WORK" 2>/dev/null | grep -c "^svc\.log\.")
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$_rot" -ge 1 ]; then
    echo "OK: rotation produced $_rot rotated copies of svc.log"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: no rotated files in $WORK:"
    ls -la "$WORK" 2>/dev/null | sed 's/^/  | /'
fi

# --- Probe 3: max-files cap is respected ----------------------------
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$_rot" -le 3 ]; then
    echo "OK: max-files cap respected ($_rot <= 3 rotated)"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: too many rotated files: $_rot > 3"
fi

# --- Probe 4: every line on disk matches log-include / not log-exclude ----
# log-exclude = ^DROP: not a single DROP-* should appear in any file.
_drops=0
for f in "$WORK"/svc.log "$WORK"/svc.log.*; do
    [ -f "$f" ] || continue
    n=$(grep -c "^DROP-" "$f" 2>/dev/null); n=${n:-0}
    _drops=$((_drops + n))
done
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$_drops" -eq 0 ]; then
    echo "OK: log-exclude dropped every DROP-* line"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: $_drops DROP-* lines leaked through log-exclude"
fi

_keeps=0
for f in "$WORK"/svc.log "$WORK"/svc.log.*; do
    [ -f "$f" ] || continue
    n=$(grep -c "^KEEP-" "$f" 2>/dev/null); n=${n:-0}
    _keeps=$((_keeps + n))
done
_TESTS_RUN=$((_TESTS_RUN + 1))
# Producer emitted 100 KEEP lines but max-files=3 caps total retention,
# so we expect at least *some* KEEP lines survived to disk.
if [ "$_keeps" -ge 1 ]; then
    echo "OK: log-include preserved $_keeps KEEP-* lines on disk"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: no KEEP-* lines survived (filters drained everything?)"
fi

# --- Probe 5: nothing un-tagged leaked either -----------------------
# Any line that wasn't KEEP-* or DROP-* indicates the filters let
# something else through (e.g. an unrelated shell trace).
_other=0
for f in "$WORK"/svc.log "$WORK"/svc.log.*; do
    [ -f "$f" ] || continue
    n=$(grep -cvE "^(KEEP-|DROP-)" "$f" 2>/dev/null); n=${n:-0}
    _other=$((_other + n))
done
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$_other" -eq 0 ]; then
    echo "OK: log-include kept the stream clean (no untagged lines)"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: $_other lines leaked past the include regex"
    for f in "$WORK"/svc.log "$WORK"/svc.log.*; do
        [ -f "$f" ] && grep -vE "^(KEEP-|DROP-)" "$f" 2>/dev/null | head -3 | sed "s|^|  $f: |"
    done
fi

test_summary
