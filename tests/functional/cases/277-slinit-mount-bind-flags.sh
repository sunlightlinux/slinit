#!/bin/sh
# Test: slinit-mount applies the per-mount flags of a bind mount.
#
# options= became one mount(2) call with MS_BIND|flags, and the kernel
# ignores every flag but MS_REC on a bind: `bind,ro,noexec` gave a
# writable, executable view of the source. A bind naming such flags is
# now followed by a MS_REMOUNT|MS_BIND call, as mount(8) does.
#
# The mount is an autofs one, set up by slinit-mount and made on first
# access, so the check goes through the whole daemon path.

KO=/lib/slinit-test-modules/autofs4.ko
if ! grep -qw autofs /proc/filesystems; then
    [ -f "$KO" ] || skip_case "no autofs in the kernel and no autofs4.ko in the image"
    insmod "$KO" 2>/tmp/277.insmod ||
        skip_case "autofs4.ko does not load: $(cat /tmp/277.insmod)"
fi

B=/srv/277
rm -rf $B
mkdir -p $B/units $B/src/data
printf '%s\n' '#!/bin/sh' 'echo ran' > $B/src/data/run.sh
chmod 755 $B/src/data/run.sh
printf '%s\n' 'what=/srv/277/src' 'where=/srv/277/auto' 'type=none' \
    'options=bind,ro,noexec' > $B/units/bindro.mount

# As a slinit service, the way it is deployed: the autofs mount names the
# daemon's pid as its process group, so the daemon has to lead one, and a
# `&` job of this non-interactive shell does not (its own mkdir under the
# autofs mount then waits on itself).
printf '%s\n' 'type = process' \
    'command = /usr/bin/slinit-mount -d /srv/277/units --foreground' \
    'logfile = /tmp/277.log' 'restart = false' > /etc/slinit.d/test-slinit-mount
slinitctl --system start test-slinit-mount >/dev/null 2>&1
_i=0
while ! grep -q " $B/auto " /proc/self/mountinfo && [ "$_i" -lt 20 ]; do
    sleep 0.25; _i=$((_i + 1))
done
assert_contains "$(grep " $B/auto " /proc/self/mountinfo)" "autofs" \
    "slinit-mount set up the autofs trigger"

# First access mounts $B/src/data on $B/auto/data.
timeout 5 ls $B/auto/data >/dev/null 2>&1; echo "first access rc=$?"
_mi=$(awk -v t="$B/auto/data" '$5 == t' /proc/self/mountinfo)
echo "mountinfo: $_mi"
echo "slinit-mount log:"; sed 's/^/  | /' /tmp/277.log

assert_contains "$_mi" "$B/auto/data" "the access made the bind mount"
# Field 6 holds the per-mount options.
_opts=$(echo "$_mi" | awk '{ print $6 }')
assert_contains ",$_opts," ",ro," "the bind mount is read-only"
assert_contains ",$_opts," ",noexec," "the bind mount is noexec"

_TESTS_RUN=$((_TESTS_RUN + 1))
if touch $B/auto/data/written 2>/dev/null; then
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: a write through the ro bind succeeded"
else
    echo "OK: a write through the ro bind is refused"
fi

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$($B/auto/data/run.sh 2>/dev/null)" = "ran" ]; then
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: a script on the noexec bind was executed"
else
    echo "OK: executing from the noexec bind is refused"
fi

# The source itself stays writable: the flags belong to the bind only.
_TESTS_RUN=$((_TESTS_RUN + 1))
if touch $B/src/data/written 2>/dev/null; then
    echo "OK: the source directory is still writable"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: the source lost its write access"
fi

slinitctl --system stop test-slinit-mount >/dev/null 2>&1
test_summary
