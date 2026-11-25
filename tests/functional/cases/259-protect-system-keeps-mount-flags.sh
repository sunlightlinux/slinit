#!/bin/sh
# 259-protect-system-keeps-mount-flags: the read-only remount under
# protect-system keeps a mount's own noexec/nosuid/nodev. Remounting with
# MS_RDONLY alone dropped them, so a noexec filesystem became executable
# inside the sandbox.

mkdir -p /srv/zz259
mount -t tmpfs -o noexec,nosuid,nodev zz259 /srv/zz259 || skip_case "cannot mount a tmpfs under /srv"
cat > /run/259-probe.sh <<'PROBE'
#!/bin/sh
awk '$5 == "/srv/zz259" { print $6 }' /proc/self/mountinfo > /run/259.result
exec sleep 60
PROBE
rm -f /run/259.result
slinitctl --system start flagprobe > /dev/null 2>&1
wait_for_service "flagprobe" "STARTED" 5
n=0
while [ ! -s /run/259.result ] && [ "$n" -lt 30 ]; do sleep 0.1; n=$((n + 1)); done
opts=$(cat /run/259.result 2>/dev/null)
echo "mount options in the sandbox: $opts"
assert_contains ",$opts," ",ro," "sub-mount read-only"
assert_contains ",$opts," ",noexec," "noexec kept"
assert_contains ",$opts," ",nosuid," "nosuid kept"
assert_contains ",$opts," ",nodev," "nodev kept"
slinitctl --system stop flagprobe > /dev/null 2>&1
umount /srv/zz259 2>/dev/null

test_summary
