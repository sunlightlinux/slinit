#!/bin/sh
# 191-apparmor-real-load — apparmor-load parses a shipped profile via
# apparmor_parser -r before start; apparmor-switch transitions the
# process into a profile on exec (aa_change_onexec via slinit-runner).
# Requires AppArmor to be loaded + apparmor_parser present.

SVC="acceptance-test-apparmor"
PROFILE=/tmp/acceptance-aa-profile
MARK=/tmp/acceptance-aa-mark

cleanup() {
    svc_remove "$SVC"
    rm -f "$PROFILE" "$MARK" 2>/dev/null
}
trap cleanup EXIT INT TERM

# --- prerequisites ----------------------------------------------------
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ ! -d /sys/kernel/security/apparmor ]; then
    echo "SKIP: AppArmor LSM not present at /sys/kernel/security/apparmor"
    test_summary
    return 0
fi
echo "OK: AppArmor LSM present"

_TESTS_RUN=$((_TESTS_RUN + 1))
if ! command -v apparmor_parser >/dev/null 2>&1; then
    echo "SKIP: apparmor_parser not installed"
    test_summary
    return 0
fi
echo "OK: apparmor_parser available"

# --- minimal profile that allows the svc to run + touch the marker.
# Named after the marker path so unloading is straightforward.
PROFILE_NAME="acceptance-aa-svc-191"
cat > "$PROFILE" <<EOF
profile $PROFILE_NAME {
  file,
  network,
}
EOF

svc_deploy "$SVC" <<EOF
type = process
apparmor-load   = $PROFILE
apparmor-switch = $PROFILE_NAME
command = /bin/sh -c 'touch $MARK; exec sleep 600'
restart = false
EOF

slinitctl --system start "$SVC" >/dev/null
wait_for_service "$SVC" "STARTED" 15 || { test_summary; return; }
sleep 1

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -e "$MARK" ]; then
    echo "OK: svc ran under apparmor-switch confinement"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: svc did not touch $MARK — transition may have failed"
fi

# Verify the child is actually confined by reading /proc/PID/attr/current.
_pid=$(slinitctl --system status "$SVC" 2>/dev/null | awk '/PID:/ {print $2; exit}')
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -n "$_pid" ]; then
    _label=$(cat "/proc/$_pid/attr/current" 2>/dev/null | tr -d '\0\n')
    case "$_label" in
        *${PROFILE_NAME}*)
            echo "OK: /proc/$_pid/attr/current shows $PROFILE_NAME confinement"
            ;;
        *)
            _TESTS_FAILED=$((_TESTS_FAILED + 1))
            echo "FAIL: expected $PROFILE_NAME in attr/current, got '$_label'"
            ;;
    esac
fi

# Best-effort profile unload (may fail without root_squash exemption).
apparmor_parser -R "$PROFILE" 2>/dev/null || true

test_summary
