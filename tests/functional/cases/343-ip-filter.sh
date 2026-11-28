#!/bin/sh
# Test: ip-address-allow / ip-address-deny, the cgroup eBPF IP filter
# (4.1), under PID 1.
#
# ip-address-deny = any with ip-address-allow = 127.0.0.1: the service
# reaches 127.0.0.1 and not 127.0.0.2 (the loopback too, so the
# difference is the filter alone). A service without the filter
# reaches both; one with ip-address-deny = 127.0.0.2 alone reaches
# 127.0.0.1. The filter leaves netlink alone: the filtered service can
# still list links.

if [ ! -f /sys/fs/cgroup/cgroup.controllers ]; then
    echo "SKIP: no cgroup v2"
    exit 77
fi
ip link set lo up 2>/dev/null

cat > /run/test-343-probe.sh <<'PROBE'
for a in 127.0.0.1 127.0.0.2; do
    if ping -c 1 -W 1 "$a" >>/run/test-343-err 2>&1; then echo "$a ok"; else echo "$a blocked"; fi
done
if ip link show lo >>/run/test-343-err 2>&1; then echo "netlink ok"; else echo "netlink blocked"; fi
PROBE

cat > /etc/slinit.d/test-343-allow <<'SVC'
type = scripted
command = /bin/sh -c "/bin/sh /run/test-343-probe.sh > /run/test-343-allow 2>&1"
slice = test343.slice
ip-address-deny = any
ip-address-allow = 127.0.0.1
SVC
cat > /etc/slinit.d/test-343-deny <<'SVC'
type = scripted
command = /bin/sh -c "/bin/sh /run/test-343-probe.sh > /run/test-343-deny 2>&1"
slice = test343.slice
ip-address-deny = 127.0.0.2
SVC
cat > /etc/slinit.d/test-343-none <<'SVC'
type = scripted
command = /bin/sh -c "/bin/sh /run/test-343-probe.sh > /run/test-343-none 2>&1"
slice = test343.slice
SVC

for s in allow deny none; do
    slinitctl --system start test-343-$s >/dev/null 2>&1 || slinitctl --system catlog test-343-$s 2>&1 | tail -3
done
a=$(cat /run/test-343-allow 2>/dev/null)
d=$(cat /run/test-343-deny 2>/dev/null)
n=$(cat /run/test-343-none 2>/dev/null)
assert_contains "$n" "127.0.0.2 ok" "no filter: 127.0.0.2 answers (the rig works)"
assert_contains "$a" "127.0.0.1 ok" "deny any + allow 127.0.0.1: 127.0.0.1 answers"
assert_contains "$a" "127.0.0.2 blocked" "deny any + allow 127.0.0.1: 127.0.0.2 is blocked"
assert_contains "$a" "netlink ok" "the IP filter leaves netlink alone"
assert_contains "$d" "127.0.0.1 ok" "deny 127.0.0.2 alone: 127.0.0.1 answers"
assert_contains "$d" "127.0.0.2 blocked" "deny 127.0.0.2 alone: 127.0.0.2 is blocked"

# A reload that takes the filter away, then a restart, on a cgroup kept
# alive by a child: the old programs go with it.
mkdir -p /sys/fs/cgroup/test343.slice/test-343-allow/keep
sed -i '/^ip-address-/d' /etc/slinit.d/test-343-allow
slinitctl --system reload test-343-allow >/dev/null 2>&1
slinitctl --system restart test-343-allow >/dev/null 2>&1
assert_contains "$(cat /run/test-343-allow 2>/dev/null)" "127.0.0.2 ok" "after the filter was removed: 127.0.0.2 answers"

test_summary
