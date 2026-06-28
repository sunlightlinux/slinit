#!/bin/sh
# 63-fdstore — file-descriptor-store config + $NOTIFY_SOCKET handoff.
#
# `file-descriptor-store-max=N` (systemd #14 parity) opens a per-service
# Unix datagram socket at /run/slinit/notify/<svc>.sock and exports its
# path as $NOTIFY_SOCKET to the child. The child can then send
# sd_notify FDSTORE=1 packets with SCM_RIGHTS to stash fds across restart.
#
# This case covers the parser + listener-bind + env-handoff path. The
# full sd_notify round-trip (FDSTORE=1 ⇒ LISTEN_FDS=N on next start)
# needs a tool that can sendmsg() SCM_RIGHTS into the listener socket;
# probing it from outside the daemon currently reproduces a delivery
# gap — strace -p $(pidof slinit) shows the listener bound but never
# triggering a non-EAGAIN recvmsg even though sendto() into the socket
# returns success. Tracked separately; this case keeps the parts that
# DO work locked down so a regression in either of them gets caught.

SVC="acceptance-test-fdstore"
SVCFILE="/etc/slinit.d/$SVC"
ENVDUMP="/tmp/acceptance-fdstore.env"
MARKER="/tmp/acceptance-fdstore.mark"

cleanup() {
    slinitctl --system stop "$SVC" 2>/dev/null
    slinitctl --system unload "$SVC" 2>/dev/null
    rm -f "$SVCFILE" "$ENVDUMP" "$MARKER"
}
trap cleanup EXIT INT TERM
cleanup

cat > "$SVCFILE" <<EOF
type = process
command = /bin/sh -c 'env > $ENVDUMP; touch $MARKER; exec sleep 60'
file-descriptor-store-max = 2
restart = false
EOF

# --- Probe 1: slinit-check accepts the directive --------------------
_chk=$(slinit-check -d /etc/slinit.d "$SVC" 2>&1)
_crc=$?
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$_crc" -eq 0 ]; then
    echo "OK: slinit-check accepts file-descriptor-store-max=2"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: slinit-check rejected the config (rc=$_crc):"
    echo "$_chk" | sed 's/^/  | /'
fi

# --- Probe 2: slinit-check rejects a negative max -------------------
sed -i 's/file-descriptor-store-max = 2/file-descriptor-store-max = -3/' "$SVCFILE"
_chk2=$(slinit-check -d /etc/slinit.d "$SVC" 2>&1)
_crc2=$?
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$_crc2" -ne 0 ] && echo "$_chk2" | grep -qi "non-negative"; then
    echo "OK: slinit-check rejects negative max (rc=$_crc2)"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: negative max not rejected (rc=$_crc2, out='$_chk2')"
fi

# Restore a valid value for the runtime probes below.
sed -i 's/file-descriptor-store-max = -3/file-descriptor-store-max = 2/' "$SVCFILE"

# --- Probe 3: start the service; env dump captures NOTIFY_SOCKET ----
slinitctl --system start "$SVC" >/dev/null 2>&1
_e=0
while [ "$_e" -lt 8 ]; do
    [ -e "$MARKER" ] && break
    sleep 1
    _e=$((_e + 1))
done

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -e "$MARKER" ]; then
    echo "OK: $SVC started and dumped its env"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: $SVC marker missing — never started?"
    test_summary
    exit 1
fi

# --- Probe 4: NOTIFY_SOCKET points at the per-service path ----------
_ns=$(grep '^NOTIFY_SOCKET=' "$ENVDUMP" | head -1)
_TESTS_RUN=$((_TESTS_RUN + 1))
case "$_ns" in
    "NOTIFY_SOCKET=/run/slinit/notify/$SVC.sock")
        echo "OK: NOTIFY_SOCKET=/run/slinit/notify/$SVC.sock"
        ;;
    *)
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: NOTIFY_SOCKET wrong or missing: '$_ns'"
        ;;
esac

# --- Probe 5: the listener socket is actually on disk ---------------
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -S "/run/slinit/notify/$SVC.sock" ]; then
    echo "OK: listener socket bound at /run/slinit/notify/$SVC.sock"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: /run/slinit/notify/$SVC.sock missing or wrong type"
    ls -la "/run/slinit/notify/" 2>&1 | sed 's/^/  | /'
fi

# --- Probe 6: LISTEN_FDS is NOT set on the *first* start ------------
# Stored fds are empty on a clean first start — the env should not
# advertise LISTEN_FDS at all (a 0 would also be wrong: systemd's
# convention is "absent" when there are no fds).
_lf=$(grep '^LISTEN_FDS=' "$ENVDUMP" | head -1)
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -z "$_lf" ]; then
    echo "OK: no LISTEN_FDS env on first start (store is empty)"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: LISTEN_FDS leaked on first start: '$_lf'"
fi

test_summary
