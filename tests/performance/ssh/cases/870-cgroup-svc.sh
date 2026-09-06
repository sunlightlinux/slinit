# 870-cgroup-svc — startup cost of a svc with an explicit cgroup
# placement. slinit creates the cgroup path + writes the svc's
# pid into cgroup.procs. Delta vs baseline = cgroup mkdir +
# write cost.
_name="perf-cgroup-$$"
_svcfile="/etc/slinit.d/$_name"
cat > "$_svcfile" <<EOF
type = scripted
command = /bin/true
cgroup = /perftest.slice
EOF
slinitctl load "$_name" > /dev/null 2>&1 || true

perf_run_iters "$ITERS" "CtlStart_cgroup_svc" "slinitctl start $_name"

slinitctl unload "$_name" > /dev/null 2>&1
rm -f "$_svcfile"
# Best-effort cleanup of the cgroup itself
rmdir /sys/fs/cgroup/perftest.slice 2>/dev/null || true
