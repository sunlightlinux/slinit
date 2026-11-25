#!/bin/sh
# 255-protect-system-submount: protect-system = strict makes the tree read-only
# recursively. The remount was not recursive, so a separately mounted
# filesystem under / (a /home, /srv, /run/user) stayed writable.

mkdir -p /srv/zz255
mount -t tmpfs zz255 /srv/zz255 || skip_case "cannot mount a tmpfs under /srv"
rm -f /run/255.result
slinitctl --system start subprobe > /dev/null 2>&1
wait_for_service "subprobe" "STARTED" 5
n=0
while [ ! -s /run/255.result ] && [ "$n" -lt 30 ]; do sleep 0.1; n=$((n + 1)); done
assert_eq "$(cat /run/255.result 2>/dev/null)" "submount-readonly" "a sub-mount is read-only under strict"
touch /srv/zz255/host 2>/dev/null
assert_eq "$?" "0" "the host still sees the sub-mount writable"
slinitctl --system stop subprobe > /dev/null 2>&1
umount /srv/zz255 2>/dev/null

test_summary
