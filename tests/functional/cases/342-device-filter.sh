#!/bin/sh
# Test: device-policy / device-allow, the cgroup eBPF device filter
# (4.1), under PID 1.
#
# closed: the pseudo devices open, /dev/kmsg does not (EPERM from the
# filter, though the service runs as root). strict with device-allow =
# /dev/kmsg r: kmsg opens for reading, not writing, and /dev/null does
# not open at all. A service without a filter opens kmsg. A reload
# widening the list, then a restart, replaces the program.

if [ ! -f /sys/fs/cgroup/cgroup.controllers ]; then
    echo "SKIP: no cgroup v2"
    exit 77
fi

cat > /run/test-342-probe.sh <<'PROBE'
for d in /dev/null /dev/kmsg; do
    if (exec 3<"$d") 2>>/run/test-342-err; then echo "$d r ok"; else echo "$d r denied"; fi
done
if (exec 3>/dev/kmsg) 2>>/run/test-342-err; then echo "kmsg w ok"; else echo "kmsg w denied"; fi
PROBE

cat > /etc/slinit.d/test-342-closed <<'SVC'
type = scripted
command = /bin/sh -c "/bin/sh /run/test-342-probe.sh > /run/test-342-closed 2>&1"
slice = test342.slice
device-policy = closed
SVC
cat > /etc/slinit.d/test-342-strict <<'SVC'
type = scripted
command = /bin/sh -c "/bin/sh /run/test-342-probe.sh > /run/test-342-strict 2>&1"
slice = test342.slice
device-policy = strict
device-allow = /dev/kmsg r
SVC
cat > /etc/slinit.d/test-342-none <<'SVC'
type = scripted
command = /bin/sh -c "/bin/sh /run/test-342-probe.sh > /run/test-342-none 2>&1"
slice = test342.slice
SVC

for s in closed strict none; do
    slinitctl --system start test-342-$s >/dev/null 2>&1 || slinitctl --system catlog test-342-$s 2>&1 | tail -3
done

c=$(cat /run/test-342-closed 2>/dev/null)
s=$(cat /run/test-342-strict 2>/dev/null)
n=$(cat /run/test-342-none 2>/dev/null)
assert_contains "$c" "/dev/null r ok" "closed: /dev/null opens"
assert_contains "$c" "/dev/kmsg r denied" "closed: /dev/kmsg is denied to a root service"
assert_contains "$s" "/dev/kmsg r ok" "strict: the allowed /dev/kmsg opens for reading"
assert_contains "$s" "kmsg w denied" "strict: not for writing (r only)"
assert_contains "$s" "/dev/null r denied" "strict: /dev/null is not allowed"
assert_contains "$n" "/dev/kmsg r ok" "no filter: /dev/kmsg opens"

# A reload that widens the list, then a restart: the new program
# replaces the old on the same cgroup, rather than both applying (the
# old one alone would still deny /dev/null). An empty child cgroup
# keeps the service's cgroup from being removed at the stop, as
# leftover processes or a delegated payload's cgroups would.
mkdir -p /sys/fs/cgroup/test342.slice/test-342-strict/keep
echo "device-allow = /dev/null rw" >> /etc/slinit.d/test-342-strict
slinitctl --system reload test-342-strict >/dev/null 2>&1
slinitctl --system restart test-342-strict >/dev/null 2>&1
s2=$(cat /run/test-342-strict 2>/dev/null)
assert_contains "$s2" "/dev/null r ok" "after reload + restart: the widened filter replaced the old one"
assert_contains "$s2" "kmsg w denied" "after reload + restart: still a filter"
assert_eq "$(test -d /sys/fs/cgroup/test342.slice/test-342-strict/keep && echo kept)" "kept" "the cgroup was the same one"

# A reload that takes the filter away, then a restart on the same
# cgroup: the old program goes with it.
sed -i '/^device-/d' /etc/slinit.d/test-342-strict
slinitctl --system reload test-342-strict >/dev/null 2>&1
slinitctl --system restart test-342-strict >/dev/null 2>&1
assert_contains "$(cat /run/test-342-strict 2>/dev/null)" "kmsg w ok" "after the filter was removed: no filter left on the cgroup"

test_summary
