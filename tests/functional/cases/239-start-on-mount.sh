#!/bin/sh
# 239-start-on-mount — a service starts when a filesystem is mounted.
#
# This trigger exists because inotify CANNOT see a mount: mounting
# creates no directory entry, so start-on-path-* never fires for one.
# Measured before this was written — an IN_ALL_EVENTS watch on the
# mountpoint reported nothing at all across a tmpfs mount, while
# poll(2) on /proc/self/mountinfo woke immediately. That poll is the
# mechanism here.
#
# The services live in the .d fixture and are reached through
# `boot waits-for:`, which is what arms a watcher; `slinitctl load`
# does not fire OnServiceLoaded for a service the set already holds.
# They are `manual = yes` so only the trigger can start them —
# otherwise a normal boot start would be indistinguishable from the
# trigger firing. Both points are the same pattern case 72 uses for
# path activation.

mkdir -p /mnt/trigger /mnt/elsewhere

# Nothing is mounted on the trigger path yet, so both services must be
# sitting still. Without this the later assertions could pass against a
# service that had simply started on its own.
_state=$(slinitctl --system status mount-svc 2>/dev/null | awk '/^ *State:/ { print $2; exit }')
echo "INFO: before any mount: mount-svc=$_state marker=$([ -f /run/mount-marker ] && echo yes || echo no)"
assert_eq "$_state" "STOPPED" "the service waits while its path is unmounted"

# The wildcard's "does not count filesystems already mounted" property is
# NOT asserted here, and cannot be: the boot sequence itself mounts
# /dev/pts and friends after the watcher reads its first table, so those
# are genuinely new mounts and firing for them is correct. An earlier
# version of this case read the resulting marker as a bug in arming.
# The arm-time behaviour is pure logic and is covered by
# TestAddDoesNotFireForAnyMountOnArm in pkg/mountwatch.
#
# What the VM can show is that a fresh mount fires it, so the marker is
# cleared first and the assertion is made against a mount this case
# performs itself.
rm -f /run/mount-any-marker
slinitctl --system --ignore-unstarted stop mount-any-svc > /dev/null 2>&1

# --- the path form ---
mount -t tmpfs none /mnt/trigger 2>&1 | sed 's/^/INFO: mount: /'
_w=0
while [ "$_w" -lt 40 ]; do
    [ -f /run/mount-marker ] && break
    sleep 0.25
    _w=$((_w + 1))
done
echo "INFO: after mounting /mnt/trigger: marker=$([ -f /run/mount-marker ] && echo yes || echo no) (${_w} ticks)"

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -f /run/mount-marker ]; then
    echo "OK: mounting the path started the service"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: the service did not start when its path was mounted"
fi

# --- the wildcard form, fired by the same mount ---
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -f /run/mount-any-marker ]; then
    echo "OK: the wildcard fired for a new mount"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: the wildcard did not fire for a new mount"
fi

# --- a mount somewhere else must not fire the path-specific service ---
rm -f /run/mount-marker
slinitctl --system --ignore-unstarted stop mount-svc > /dev/null 2>&1
mount -t tmpfs none /mnt/elsewhere 2>/dev/null
sleep 2
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -f /run/mount-marker ]; then
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: an unrelated mount fired a path-specific trigger"
else
    echo "OK: an unrelated mount does not fire a path-specific trigger"
fi

# --- a remount fires again: the trigger is a transition, not a one-shot ---
umount /mnt/trigger 2>/dev/null
sleep 1
rm -f /run/mount-marker
mount -t tmpfs none /mnt/trigger 2>/dev/null
_w=0
while [ "$_w" -lt 40 ]; do
    [ -f /run/mount-marker ] && break
    sleep 0.25
    _w=$((_w + 1))
done
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -f /run/mount-marker ]; then
    echo "OK: an unmount/remount cycle fires the trigger again"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: the trigger did not fire a second time after a remount"
fi

umount /mnt/trigger /mnt/elsewhere 2>/dev/null

# `show` must render the directive: an operator with a service that is
# not firing needs to see what it was configured with.
_shown=$(slinitctl --system show mount-svc 2>/dev/null | grep "^StartOnMount=")
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$_shown" = "StartOnMount=/mnt/trigger" ]; then
    echo "OK: show renders the configured mount trigger"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: show gave [$_shown]"
fi

# --- a relative path is a load error, not a trigger that never fires ---
cat > /etc/slinit.d/test-mount-bad <<'EOF'
type = scripted
command = /bin/true
start-on-mount = mnt/relative
EOF
_lint=$(slinit-check -d /etc/slinit.d test-mount-bad 2>&1)
_TESTS_RUN=$((_TESTS_RUN + 1))
if echo "$_lint" | grep -q "start-on-mount"; then
    echo "OK: a relative path is refused, and the reason names the directive"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: a relative path was not refused by name: $_lint"
fi
rm -f /etc/slinit.d/test-mount-bad

test_summary
