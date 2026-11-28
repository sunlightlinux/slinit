#!/bin/sh
# Test: private-devices (systemd's PrivateDevices=), 4.1.
#
# The service sees a /dev with the pseudo devices, which work, and no
# physical device: /dev/kmsg, present on the host, is gone, and so is
# CAP_MKNOD from its bounding set.

cat > /etc/slinit.d/test-339 <<'SVC'
type = scripted
command = /bin/sh -c "ls /dev > /run/test-339.ls; echo ok > /dev/null && echo null-ok >> /run/test-339.ls; grep CapBnd /proc/self/status >> /run/test-339.ls"
private-devices = yes
SVC
assert_exit_code "test -e /dev/kmsg" 0 "the host has /dev/kmsg"
slinitctl --system start test-339 >/dev/null 2>&1
out=$(cat /run/test-339.ls 2>/dev/null)
assert_contains "$out" "null-ok" "/dev/null works in the private /dev"
assert_contains "$out" "urandom" "the pseudo devices are there"
assert_not_contains "$out" "kmsg" "no /dev/kmsg in the private /dev"
bnd=$(echo "$out" | awk '/CapBnd/ {print $2}')

# The private /dev cannot be undone: a root service that unmounts it
# finds an empty directory, not the host's devices.
cat > /etc/slinit.d/test-339-umount <<'SVC'
type = scripted
command = /bin/sh -c "umount -l /dev 2>/dev/null; { ls /dev; echo ls-done; } > /run/test-339-umount.ls"
private-devices = yes
SVC
slinitctl --system start test-339-umount >/dev/null 2>&1
assert_contains "$(cat /run/test-339-umount.ls 2>/dev/null)" "ls-done" "the umount check ran"
assert_not_contains "$(cat /run/test-339-umount.ls 2>/dev/null)" "kmsg" "unmounting the private /dev does not bring the host's back"
_TESTS_RUN=$((_TESTS_RUN + 1))
# CAP_MKNOD is bit 27.
if [ -n "$bnd" ] && [ $(( 0x$bnd >> 27 & 1 )) -eq 0 ]; then
    echo "OK: CAP_MKNOD dropped (CapBnd $bnd)"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: CAP_MKNOD still bounded (CapBnd '$bnd')"
fi

test_summary
