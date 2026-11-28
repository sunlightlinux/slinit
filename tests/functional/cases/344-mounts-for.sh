#!/bin/sh
# Test: assert-mounts-for (4.1, systemd's RequiresMountsFor=) under
# PID 1.
#
# /etc/fstab names /mnt/t344 (noauto). A service asserting
# mounts-for /mnt/t344/data fails to start while it is not mounted,
# and starts once it is; one asserting a path no fstab mount point is
# on the way to starts either way.

mkdir -p /mnt/t344
cp /etc/fstab /run/test-344-fstab.bak 2>/dev/null || : > /run/test-344-fstab.bak
echo "tmpfs /mnt/t344 tmpfs noauto 0 0" >> /etc/fstab

cat > /etc/slinit.d/test-344 <<'SVC'
type = scripted
command = /bin/true
assert-mounts-for = /mnt/t344/data
SVC
cat > /etc/slinit.d/test-344-other <<'SVC'
type = scripted
command = /bin/true
assert-mounts-for = /usr/share
SVC

slinitctl --system start test-344 >/dev/null 2>&1
st=$(slinitctl --system status test-344 2>&1)
assert_not_contains "$st" "STARTED" "not mounted: the start fails"
slinitctl --system start test-344-other >/dev/null 2>&1
assert_contains "$(slinitctl --system status test-344-other 2>&1)" "STARTED" "a path no fstab mount point is on starts"

mount /mnt/t344
slinitctl --system start test-344 >/dev/null 2>&1
assert_contains "$(slinitctl --system status test-344 2>&1)" "STARTED" "mounted: the start goes on"

umount /mnt/t344 2>/dev/null
cp /run/test-344-fstab.bak /etc/fstab
test_summary
