#!/bin/sh
# 96-initd-lsb-autodetect — /etc/init.d auto-detect for LSB-headers
# scripts (### BEGIN INIT INFO ### style).
#
# Companion to case 83 which covers OpenRC-style depend() — this one
# drops a plain SysV script with an LSB `Provides:` + `Required-Start:`
# header and confirms the deps land on the ServiceDescription.

INITD_DIR="/etc/init.d"
INITD_SCRIPT="${INITD_DIR}/acceptance-test-lsb-init"
NEED_SVC="${ACCEPTANCE_NS_PREFIX}lsb-need"
MARKER_START="/tmp/acceptance-lsb-svc-started"
MARKER_NEED="/tmp/acceptance-lsb-need-started"

cleanup() {
    slinitctl --system --ignore-unstarted stop acceptance-test-lsb-init 2>/dev/null || true
    slinitctl --system unload acceptance-test-lsb-init 2>/dev/null || true
    rm -f "$INITD_SCRIPT" "$MARKER_START" "$MARKER_NEED"
    svc_remove "$NEED_SVC"
}
trap cleanup EXIT INT TERM
cleanup

# Same probe pattern as case 83: only /etc/init.d/ existing AT SLINIT
# BOOT arms the fallback.
mkdir -p "$INITD_DIR"
cat > "${INITD_DIR}/acceptance-test-initd-probe" <<'PROBE'
#!/bin/sh
### BEGIN INIT INFO
# Provides: acceptance-test-initd-probe
### END INIT INFO
exit 0
PROBE
chmod 755 "${INITD_DIR}/acceptance-test-initd-probe"

_probe_err=$(slinitctl --system start acceptance-test-initd-probe 2>&1 || true)
slinitctl --system --ignore-unstarted stop acceptance-test-initd-probe 2>/dev/null || true
slinitctl --system unload acceptance-test-initd-probe 2>/dev/null || true
rm -f "${INITD_DIR}/acceptance-test-initd-probe"

_TESTS_RUN=$((_TESTS_RUN + 1))
case "$_probe_err" in
    *"could not be loaded"*)
        echo "SKIP: init.d fallback disabled at slinit boot"
        test_summary
        exit 0
        ;;
    *)
        echo "OK: init.d fallback armed"
        ;;
esac

# Dep target — native slinit service.
svc_deploy "$NEED_SVC" <<EOF
type = scripted
command = /bin/sh -c 'echo started > $MARKER_NEED'
stop-command = /bin/sh -c 'rm -f $MARKER_NEED'
EOF

# LSB-style init.d. Deliberately no OpenRC shebang so the loader
# takes the LSB parse path.
cat > "$INITD_SCRIPT" <<EOF
#!/bin/sh
### BEGIN INIT INFO
# Provides:          acceptance-test-lsb-init
# Required-Start:    $NEED_SVC
# Required-Stop:     $NEED_SVC
# Default-Start:     2 3 4 5
# Default-Stop:      0 1 6
# Short-Description: 96-initd-lsb-autodetect fixture
# Description:       LSB-headers init.d script exercising slinit's
#                    ### BEGIN INIT INFO ### parse fallback.
### END INIT INFO

case "\$1" in
  start)
    echo started > $MARKER_START
    ;;
  stop)
    rm -f $MARKER_START
    ;;
  *)
    echo "usage: \$0 {start|stop}" >&2
    exit 1
    ;;
esac
exit 0
EOF
chmod 755 "$INITD_SCRIPT"

assert_eq "$(svc_state "$NEED_SVC")" "STOPPED" "need target starts out STOPPED"

slinitctl --system start acceptance-test-lsb-init 2>/dev/null
sleep 2

wait_for_service acceptance-test-lsb-init STARTED 10
assert_eq "$(svc_state acceptance-test-lsb-init)" "STARTED" \
    "LSB init.d service reached STARTED"
assert_eq "$(svc_state "$NEED_SVC")" "STARTED" \
    "need target auto-started via LSB Required-Start"

assert_eq "$(cat $MARKER_START 2>/dev/null)" "started" \
    "LSB script start block executed on the target"
assert_eq "$(cat $MARKER_NEED 2>/dev/null)" "started" \
    "need target's own command ran"

slinitctl --system stop acceptance-test-lsb-init 2>/dev/null
sleep 1
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ ! -e "$MARKER_START" ]; then
    echo "OK: LSB script stop block executed"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: marker still present after stop"
fi

test_summary
