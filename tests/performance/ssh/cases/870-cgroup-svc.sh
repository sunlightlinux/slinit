# 870-cgroup-svc — start + stop of a service with its own cgroup, against
# the plain one in 710: the difference is making the cgroup and moving the
# process into it. The path has to lie under /sys/fs/cgroup; any other is
# only warned about, and the service runs without one.
_name="perf-cgroup-$$"
_svcfile="/etc/slinit.d/$_name"
_cg="/sys/fs/cgroup/perftest.slice"
cat > "$_svcfile" <<EOS
type = process
command = /bin/sleep 3600
restart = no
cgroup = $_cg
EOS
perf_run_iters "$ITERS" "CtlStartStop_cgroup_svc" \
    "slinitctl start $_name && slinitctl stop $_name"
slinitctl unload "$_name" > /dev/null 2>&1
rm -f "$_svcfile"
rmdir "$_cg" 2>/dev/null || true
