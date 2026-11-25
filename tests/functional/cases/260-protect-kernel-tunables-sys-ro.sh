#!/bin/sh
# 260-protect-kernel-tunables-sys-ro: protect-kernel-tunables makes /sys
# read-only too, as systemd's ProtectKernelTunables= does; only parts of
# /proc were covered.

mountpoint -q /sys 2>/dev/null || grep -q ' /sys ' /proc/self/mountinfo || skip_case "/sys not mounted"
cat > /run/260-probe.sh <<'PROBE'
#!/bin/sh
# The bind stacks a new /sys over the old one: the last entry is the
# one path lookups see.
awk '$5 == "/sys" { o = $6 } END { print o }' /proc/self/mountinfo > /run/260.result
exec sleep 60
PROBE
rm -f /run/260.result
slinitctl --system start sysprobe > /dev/null 2>&1
wait_for_service "sysprobe" "STARTED" 5
n=0
while [ ! -s /run/260.result ] && [ "$n" -lt 30 ]; do sleep 0.1; n=$((n + 1)); done
opts=$(cat /run/260.result 2>/dev/null)
echo "/sys options in the sandbox: $opts"
assert_contains ",$opts," ",ro," "/sys read-only under protect-kernel-tunables"
host=$(awk '$5 == "/sys" { o = $6 } END { print o }' /proc/self/mountinfo)
assert_contains ",$host," ",rw," "host /sys untouched"
slinitctl --system stop sysprobe > /dev/null 2>&1

test_summary
