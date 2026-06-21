#!/bin/sh
# 44-run-as-chroot — `run-as = name` drops the child to the given user
# (slinit Setuid+Setgid via SysProcAttr.Credential); `chroot = /dir`
# pivots the child's root to that directory. The two combine cleanly
# because slinit chroots BEFORE setuid (parser/loader wiring) so the
# child can find /bin/sh inside the jail even as nobody.

SVC_UID="acceptance-test-runas"
SVC_CHROOT="acceptance-test-chroot"
JAIL="/run/acceptance-test-jail"
MARK_UID="/run/acceptance-test-runas.uid"
MARK_CHROOT="/run/acceptance-test-chroot.mark"

cleanup() {
    svc_remove "$SVC_UID" "$SVC_CHROOT"
    umount "$JAIL/proc" 2>/dev/null
    rm -rf "$JAIL"
    rm -f "$MARK_UID" "$MARK_CHROOT"
}
trap cleanup EXIT INT TERM

rm -f "$MARK_UID" "$MARK_CHROOT"

# --- Sub-case A: run-as nobody --------------------------------------
# nobody must exist; on glibc/musl/Void it's UID 65534. We don't hard-
# code the UID — just compare what the service stamps against what the
# host's getent says.
_nobody_uid=$(getent passwd nobody | awk -F: '{print $3}')
if [ -z "$_nobody_uid" ]; then
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: cannot resolve nobody on target — skipping run-as test"
else
    chmod 0666 "$MARK_UID" 2>/dev/null
    : > "$MARK_UID"
    chmod 0666 "$MARK_UID"

    svc_deploy "$SVC_UID" <<EOF
type = process
run-as = nobody
command = /bin/sh -c 'id -u > $MARK_UID; while :; do sleep 60; done'
restart = false
EOF
    slinitctl --system start "$SVC_UID" >/dev/null 2>&1
    wait_for_service "$SVC_UID" "STARTED" 10 || true
    assert_service_state "$SVC_UID" "STARTED" "$SVC_UID STARTED"
    sleep 1
    _got_uid=$(cat "$MARK_UID" 2>/dev/null)
    assert_eq "$_got_uid" "$_nobody_uid" "run-as nobody: child UID matches /etc/passwd"
fi

# --- Sub-case B: chroot ---------------------------------------------
# Build a minimal jail with /bin/sh (busybox or static) and a sentinel
# file. The child stamps the sentinel into the marker — if chroot took,
# it sees the jail's /sentinel, NOT the host's.
mkdir -p "$JAIL/bin"
# Copy busybox or sh statically. Trying both common locations.
if [ -x /bin/busybox ]; then
    cp /bin/busybox "$JAIL/bin/busybox"
    ln -sf busybox "$JAIL/bin/sh"
elif ldd /bin/sh 2>/dev/null | grep -q "not a dynamic"; then
    cp /bin/sh "$JAIL/bin/sh"
else
    # Carry the dynamic loader + libs alongside the shell.
    cp /bin/sh "$JAIL/bin/sh"
    for _lib in $(ldd /bin/sh 2>/dev/null | awk '/=>/ {print $3} /\/ld-/ {print $1}'); do
        [ -f "$_lib" ] || continue
        _libdir=$(dirname "$_lib")
        mkdir -p "$JAIL$_libdir"
        cp -L "$_lib" "$JAIL$_libdir/"
    done
fi
echo "JAILED_FS_SENTINEL" > "$JAIL/sentinel"

# Marker lives OUTSIDE the jail — chroot=$JAIL means the child sees
# / == $JAIL, so it can't reach $MARK_CHROOT by host path. Use a path
# inside the jail and copy back.
JAIL_MARK="/inner-mark"
chmod 0666 "$MARK_CHROOT" 2>/dev/null
: > "$JAIL$JAIL_MARK"
chmod 0666 "$JAIL$JAIL_MARK"

# Inner command uses only shell builtins (read + redirect): copying
# /bin/cat into the jail would just add libc dependencies for no
# diagnostic gain. `read` proves /sentinel is *readable from inside
# the chroot* — which is the whole claim.
svc_deploy "$SVC_CHROOT" <<EOF
type = process
chroot = $JAIL
command = /bin/sh -c 'read x < /sentinel; echo "\$\$x" > $JAIL_MARK; while :; do sleep 60; done'
restart = false
EOF
slinitctl --system start "$SVC_CHROOT" >/dev/null 2>&1
wait_for_service "$SVC_CHROOT" "STARTED" 10 || true
assert_service_state "$SVC_CHROOT" "STARTED" "$SVC_CHROOT STARTED"
sleep 1

_got_sent=$(cat "$JAIL$JAIL_MARK" 2>/dev/null)
assert_eq "$_got_sent" "JAILED_FS_SENTINEL" "chroot: child sees the jail's /sentinel"

# Negative: same content should NOT exist at host /sentinel — confirms
# the child wasn't running outside the jail.
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ ! -e /sentinel ] || ! grep -q JAILED_FS_SENTINEL /sentinel 2>/dev/null; then
    echo "OK: host /sentinel absent (chroot wasn't a no-op)"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: host has a /sentinel — can't prove chroot did anything"
fi

test_summary
