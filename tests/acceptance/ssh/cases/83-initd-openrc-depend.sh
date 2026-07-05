#!/bin/sh
# 83-initd-openrc-depend — /etc/init.d auto-detect handles OpenRC-style
# `depend()` shell functions.
#
# Alpine, Void, and Gentoo ship no LSB blocks in /etc/init.d — every one
# of their init.d scripts declares deps inside a `depend() { need X; ... }`
# shell function that slinit's loader parses via a sandboxed `sh -c`
# invocation. This case drops one such script into /etc/init.d, asks
# slinit to load it, and confirms the deps landed on the resulting
# ServiceDescription end-to-end.

INITD_DIR="/etc/init.d"
INITD_SCRIPT="${INITD_DIR}/acceptance-test-openrc-depend"
NEED_SVC="${ACCEPTANCE_NS_PREFIX}openrc-need"
MARKER_START="/tmp/acceptance-openrc-svc-started"
MARKER_NEED="/tmp/acceptance-openrc-need-started"

cleanup() {
    # Stop the openrc-flavoured init.d service first so its deps release.
    slinitctl --system --ignore-unstarted stop acceptance-test-openrc-depend 2>/dev/null || true
    slinitctl --system unload acceptance-test-openrc-depend 2>/dev/null || true
    rm -f "$INITD_SCRIPT" "$MARKER_START" "$MARKER_NEED"
    svc_remove "$NEED_SVC"
}
trap cleanup EXIT INT TERM
cleanup

# slinit reads DefaultInitDDirs (/etc/init.d, /etc/rc.d) once at boot
# and only enables the init.d fallback when at least one is present at
# that moment. If /etc/init.d was created *after* slinit came up (which
# is the case on a minimal Void deploy), auto-detect stays off and the
# rest of this case has no surface to test — skip cleanly.
#
# Probe the daemon rather than checking the directory: a directory that
# appeared post-boot still leaves the fallback disabled, so only the
# daemon's "can this service load?" answer is authoritative.
mkdir -p "$INITD_DIR"
touch "${INITD_DIR}/acceptance-test-initd-probe" && chmod +x "${INITD_DIR}/acceptance-test-initd-probe"
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
        echo "SKIP: init.d fallback disabled at slinit boot (probe couldn't load)"
        echo "      Fix: ensure $INITD_DIR exists in the rootfs BEFORE slinit starts."
        test_summary
        exit 0
        ;;
    *)
        echo "OK: init.d fallback is armed (probe loaded cleanly)"
        ;;
esac

# The dep target — a native slinit service that flips a marker file
# on start. Loaded first so `need` can resolve it at parse time.
svc_deploy "$NEED_SVC" <<EOF
type = scripted
command = /bin/sh -c 'echo started > $MARKER_NEED'
stop-command = /bin/sh -c 'rm -f $MARKER_NEED'
EOF

# The OpenRC init.d script. No LSB block; the shebang is openrc-run
# (required by LooksLikeOpenRCScript), depend() declares one hard
# `need` and one order-only `after`.
cat > "$INITD_SCRIPT" <<EOF
#!/sbin/openrc-run
# 83-initd-openrc-depend fixture. slinit's initd loader sandboxes
# depend() to extract the need/after directives even though the
# script itself is not run inside openrc-run.

description="OpenRC-style depend() acceptance fixture"

depend() {
    need $NEED_SVC
    after acceptance-test-non-existent-order-tag
}

start() {
    echo started > $MARKER_START
}

stop() {
    rm -f $MARKER_START
}
EOF
chmod 755 "$INITD_SCRIPT"

# Precondition: neither service running.
assert_eq "$(svc_state "$NEED_SVC")" "STOPPED" "need target starts out STOPPED"

# Start the OpenRC init.d service — the parsed `need` must pull
# $NEED_SVC up first, then the script's start() sets its own marker.
slinitctl --system start acceptance-test-openrc-depend 2>/dev/null
sleep 2

wait_for_service acceptance-test-openrc-depend STARTED 10
assert_eq "$(svc_state "acceptance-test-openrc-depend")" "STARTED" \
    "openrc-depend service reached STARTED"
assert_eq "$(svc_state "$NEED_SVC")" "STARTED" \
    "need target auto-started via depend() need directive"

# Both start()s wrote their marker file.
assert_eq "$(cat $MARKER_START 2>/dev/null)" "started" \
    "openrc-depend start() executed on the target"
assert_eq "$(cat $MARKER_NEED 2>/dev/null)" "started" \
    "need target start command executed"

# Stop the openrc-depend service; its stop() should run.
slinitctl --system stop acceptance-test-openrc-depend 2>/dev/null
sleep 1
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ ! -e "$MARKER_START" ]; then
    echo "OK: openrc-depend stop() executed"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: marker $MARKER_START still present after stop"
fi

test_summary
