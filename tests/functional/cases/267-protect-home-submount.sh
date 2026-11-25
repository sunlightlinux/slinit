#!/bin/sh
# 267-protect-home-submount: protect-home = read-only covers filesystems
# mounted below /home too. The bind was recursive but only the mount at the
# path was remounted read-only, so a separate filesystem below it (a user's
# own /home/<user>, /run/user/<uid>) stayed writable.

mkdir -p /home/zz267
mount -t tmpfs zz267 /home/zz267 || skip_case "cannot mount a tmpfs under /home"
cat > /run/267-probe.sh <<'PROBE'
#!/bin/sh
if touch /home/zz267/p 2>/dev/null; then echo writable; else echo readonly; fi > /run/267.result
exec sleep 60
PROBE
rm -f /run/267.result
slinitctl --system start homeprobe > /dev/null 2>&1
wait_for_service "homeprobe" "STARTED" 5
n=0
while [ ! -s /run/267.result ] && [ "$n" -lt 30 ]; do sleep 0.1; n=$((n + 1)); done
assert_eq "$(cat /run/267.result 2>/dev/null)" "readonly" "a filesystem mounted below /home is read-only under protect-home = read-only"
slinitctl --system stop homeprobe > /dev/null 2>&1
umount /home/zz267 2>/dev/null

test_summary
