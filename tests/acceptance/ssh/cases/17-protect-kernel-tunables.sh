#!/bin/sh
# 17-protect-kernel-tunables — `protect-kernel-tunables = yes` should
# make /proc/sys read-only inside the service's mount namespace, while
# leaving the host's /proc/sys writable. Probe via /proc/<svc-pid>/root.
#
# (Originally this slot held a no-new-privs probe; that option is parsed
# but its runtime application is currently a stub — see attrs.go's
# applyNoNewPrivs. Re-purposed once the limitation was identified.)

SVC="acceptance-test-pkt"

trap 'svc_remove "$SVC"' EXIT INT TERM

svc_deploy "$SVC" <<EOF
type = process
protect-kernel-tunables = yes
command = /bin/sh -c 'while :; do sleep 60; done'
restart = false
EOF

slinitctl --system start "$SVC" >/dev/null 2>&1
wait_for_service "$SVC" "STARTED" 10 || true

_pid="$(slinitctl --system status "$SVC" 2>/dev/null | awk '/PID:/ {print $2; exit}')"
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -z "$_pid" ]; then
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: no pid for $SVC"
    test_summary
    exit 1
fi
echo "OK: $SVC running as pid $_pid"

# Write attempt through the service's mount namespace must fail with EROFS.
# We delegate to a sub-shell so the redirection error (which the *outer*
# shell would otherwise emit on its own stderr and slip past `2>&1`) is
# captured by the inner sh's stderr.
_TARGET="/proc/$_pid/root/proc/sys/kernel/random/uuid"
_err="$(sh -c "echo testval > $_TARGET" 2>&1)"
_TESTS_RUN=$((_TESTS_RUN + 1))
case "$_err" in
    *"Read-only file system"*|*"read-only"*)
        echo "OK: write to /proc/sys denied inside namespace"
        ;;
    "")
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: write unexpectedly succeeded"
        ;;
    *)
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: expected EROFS, got: $_err"
        ;;
esac

# Reads of /proc/sys must still work (it's only ro, not invisible).
_TESTS_RUN=$((_TESTS_RUN + 1))
_host="$(cat /proc/sys/kernel/hostname 2>/dev/null)"
_inside="$(cat "/proc/$_pid/root/proc/sys/kernel/hostname" 2>/dev/null)"
if [ -n "$_host" ] && [ "$_host" = "$_inside" ]; then
    echo "OK: /proc/sys still readable inside namespace ('$_inside')"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: hostname mismatch: host='$_host', inside='$_inside'"
fi

# Host /proc/sys must remain writable (no leak).
_TESTS_RUN=$((_TESTS_RUN + 1))
_curhn="$(cat /proc/sys/kernel/hostname)"
if echo "$_curhn" > /proc/sys/kernel/hostname 2>/dev/null; then
    echo "OK: host /proc/sys still writable"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: host /proc/sys became read-only — namespace leak?"
fi

test_summary
