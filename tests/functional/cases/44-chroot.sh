#!/bin/sh
# Test: chroot isolates service to a root directory.
# Validates: chroot setting on ProcessService.
#
# The case builds a real minimal root under /run/chroot-root — busybox,
# sh/sleep/cat as busybox links, and the musl loader busybox is linked
# against — with a marker file at its top, then starts chroot-svc into
# it. What that proves:
#   - the service reads /chroot-marker, which exists only inside the new
#     root, and writes /chroot-result, which the host finds under
#     /run/chroot-root;
#   - readlink /proc/PID/root is /run/chroot-root.
# This used to run with chroot = /, which changes nothing, so it passed
# with the directive ignored.

ROOT=/run/chroot-root
LDSO=/lib/ld-musl-x86_64.so.1

[ -x /bin/busybox ] || skip_case "no /bin/busybox to build a chroot from"
[ -e "$LDSO" ] || skip_case "no $LDSO (musl loader) to build a chroot from"

mkdir -p "$ROOT/bin" "$ROOT/lib"
cp /bin/busybox "$ROOT/bin/busybox"
cp -L "$LDSO" "$ROOT/lib/ld-musl-x86_64.so.1"
if [ -e /lib/libc.musl-x86_64.so.1 ]; then
    cp -L /lib/libc.musl-x86_64.so.1 "$ROOT/lib/libc.musl-x86_64.so.1"
fi
for a in sh sleep cat echo true; do
    ln -sf busybox "$ROOT/bin/$a"
done
_token="chroot-marker-$(date +%s)-$$"
echo "$_token" > "$ROOT/chroot-marker"
rm -f "$ROOT/chroot-result" "$ROOT/chroot-saw"

# The root must actually be usable, or a failure below would be about
# the fixture rather than about chroot.
if command -v chroot >/dev/null 2>&1; then
    _selftest=$(chroot "$ROOT" /bin/cat /chroot-marker 2>&1)
    [ "$_selftest" = "$_token" ] || skip_case "minimal root at $ROOT is not runnable: $_selftest"
fi

slinitctl --system start chroot-svc >/dev/null 2>&1
wait_for_service "chroot-svc" "STARTED" 10

# Give service time to write result
for _ in 1 2 3 4 5 6 7 8 9 10; do
    [ -s "$ROOT/chroot-saw" ] && break
    sleep 0.5
done

result=$(cat "$ROOT/chroot-result" 2>/dev/null)
assert_eq "$result" "chroot-ok" "chroot service wrote its result inside the new root"

saw=$(cat "$ROOT/chroot-saw" 2>/dev/null)
assert_eq "$saw" "$_token" "chroot service read the marker only the new root has"

_pid=$(slinitctl --system status chroot-svc 2>/dev/null | awk '/PID:/ { print $2; exit }')
_root=$(readlink "/proc/$_pid/root" 2>/dev/null)
assert_eq "$_root" "$ROOT" "/proc/PID/root of the service is the new root"

assert_service_state "chroot-svc" "STARTED" "chroot-svc is STARTED"

slinitctl --system stop chroot-svc >/dev/null 2>&1

test_summary
