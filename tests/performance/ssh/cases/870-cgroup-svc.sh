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

# stop before unload: the benchmark above leaves the service STARTED,
# and unload refuses a service that is not stopped ("service is not
# stopped"). The error went to /dev/null, so every run leaked one
# loaded service into PID 1 — 62 had accumulated on the reference
# machine, inflating every later measurement and the RSS the leak
# cases watch.
slinitctl stop "$_name" > /dev/null 2>&1
slinitctl unload "$_name" > /dev/null 2>&1
rm -f "$_svcfile"
# Best-effort cleanup of the cgroup itself
rmdir /sys/fs/cgroup/perftest.slice 2>/dev/null || true
