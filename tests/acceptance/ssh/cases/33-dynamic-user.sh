#!/bin/sh
# 33-dynamic-user — `dynamic-user = yes` allocates a transient UID/GID
# in the systemd-style 61184..65519 range for the service. The child
# runs as that uid; no entry is added to /etc/passwd. The allocation is
# tracked in pkg/service/uidpool.go.

SVC="acceptance-test-dynuser"
MARK="/run/acceptance-test-dynuser.mark"

cleanup() {
    svc_remove "$SVC"
    rm -f "$MARK"
}
trap cleanup EXIT INT TERM

# The transient dynamic-user has no write perms anywhere under /run
# (mode 755, owned by root). Pre-create the marker world-writable so the
# service can overwrite it.
#
# Single-line command — slinit's parser stops at the first newline in a
# value, so a multi-line script silently truncates.
rm -f "$MARK"
: > "$MARK"
chmod 0666 "$MARK"

svc_deploy "$SVC" <<EOF
type = process
dynamic-user = yes
command = /bin/sh -c 'id -u > $MARK; while :; do sleep 60; done'
restart = false
EOF

slinitctl --system start "$SVC" >/dev/null 2>&1
wait_for_service "$SVC" "STARTED" 10 || true
assert_service_state "$SVC" "STARTED" "$SVC STARTED"

sleep 1
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ ! -r "$MARK" ]; then
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: $MARK not written by child"
    test_summary
    exit 1
fi

_uid=$(cat "$MARK")
_TESTS_RUN=$((_TESTS_RUN + 1))
case "$_uid" in
    *[!0-9]*|"")
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: child uid not numeric: '$_uid'"
        ;;
    *)
        if [ "$_uid" -ge 61184 ] && [ "$_uid" -le 65519 ]; then
            echo "OK: child running as transient uid $_uid (in 61184..65519)"
        else
            _TESTS_FAILED=$((_TESTS_FAILED + 1))
            echo "FAIL: uid $_uid outside dynamic-user pool [61184..65519]"
        fi
        ;;
esac

# Sanity: no permanent entry was added to /etc/passwd for this uid.
# awk exits 0 when no row matches (good) and 1 when a row matches (bad);
# `if awk ...; then OK; else FAIL` reads more naturally than the inverse.
_TESTS_RUN=$((_TESTS_RUN + 1))
if awk -F: -v u="$_uid" '$3==u {print; exit 1}' /etc/passwd; then
    echo "OK: no /etc/passwd entry for transient uid $_uid"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: /etc/passwd contains a row for uid $_uid"
fi

test_summary
