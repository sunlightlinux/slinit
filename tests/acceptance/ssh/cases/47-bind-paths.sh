#!/bin/sh
# 47-bind-paths — two namespace-mount knobs:
#
#   read-only-paths = /p1 /p2  — bind /p1 over /p1 with MS_RDONLY +
#                                 MS_REMOUNT — read works, write EROFS.
#   bind-paths      = src:dst  — bind src onto dst (so a service sees
#                                 /var/log as if it were at /tmp/lookup).
#
# Probe: write into the read-only target (must fail with EROFS), then
# read from the bind destination (must show the source's contents).

SVC="acceptance-test-binds"
MARK="/run/acceptance-test-binds.log"
RO_TARGET="/run/acceptance-test-ro"
BIND_SRC="/run/acceptance-test-src.d"
BIND_DST="/run/acceptance-test-dst.d"

cleanup() {
    svc_remove "$SVC"
    rm -f "$MARK"
    rm -rf "$RO_TARGET" "$BIND_SRC" "$BIND_DST"
}
trap cleanup EXIT INT TERM

rm -f "$MARK"
rm -rf "$RO_TARGET" "$BIND_SRC" "$BIND_DST"

# Read-only target: a directory containing a sentinel readable file.
mkdir -p "$RO_TARGET"
echo "RO_READ_OK" > "$RO_TARGET/sentinel"
chmod -R 0755 "$RO_TARGET"

# Bind source/destination: source has a file; destination is empty until
# the bind. After the bind inside the service ns, dst/sentinel exists
# because dst overlays the source.
mkdir -p "$BIND_SRC" "$BIND_DST"
echo "BIND_SRC_CONTENT" > "$BIND_SRC/sentinel"

: > "$MARK"
chmod 0666 "$MARK"

# Single-line command: write attempt + read attempt, both stamped to
# the marker. Error text for the write goes to stderr; redirect 2>&1
# captures it.
svc_deploy "$SVC" <<EOF
type = process
read-only-paths = $RO_TARGET
bind-paths = $BIND_SRC:$BIND_DST
command = /bin/sh -c 'echo "--write--" > $MARK; touch $RO_TARGET/new 2>>$MARK; echo "rc=\$\$?" >> $MARK; echo "--read--" >> $MARK; cat $BIND_DST/sentinel >> $MARK 2>&1; while :; do sleep 60; done'
restart = false
EOF

slinitctl --system start "$SVC" >/dev/null 2>&1
wait_for_service "$SVC" "STARTED" 10 || true
assert_service_state "$SVC" "STARTED" "$SVC STARTED"

sleep 1

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ ! -r "$MARK" ]; then
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: $MARK not readable"
    test_summary
    exit 1
fi
echo "OK: marker written"
_log=$(cat "$MARK")
echo "$_log" | sed 's/^/  log: /'

# read-only-paths: write must have failed.
assert_contains "$_log" "Read-only" "read-only-paths: write rejected (EROFS)"

# rc=$? must be non-zero (touch failed → exit 1 typically).
_TESTS_RUN=$((_TESTS_RUN + 1))
case "$_log" in
    *"rc=0"*)
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: touch on RO path reported success"
        ;;
    *)
        echo "OK: touch on RO path returned non-zero"
        ;;
esac

# bind-paths: the bind brought BIND_SRC/sentinel under BIND_DST/sentinel
# in the service ns. The marker must contain the source's content.
assert_contains "$_log" "BIND_SRC_CONTENT" "bind-paths: dst overlays src content"

# Sanity: host's $BIND_DST/sentinel must NOT exist outside the ns
# (we never created it directly there).
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -e "$BIND_DST/sentinel" ]; then
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: $BIND_DST/sentinel leaked to host (bind escaped namespace)"
else
    echo "OK: bind confined to service mnt-ns (host $BIND_DST stays empty)"
fi

test_summary
