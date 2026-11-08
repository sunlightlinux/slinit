# 860-memory-pressure-watch — start + stop of a service that arms a PSI
# memory-pressure watcher on its cgroup. The difference from 870, the same
# service without the watcher, is arming and disarming it.
_name="perf-psi-mem-$$"
_svcfile="/etc/slinit.d/$_name"
_cg="/sys/fs/cgroup/perftest-psi.slice"
cat > "$_svcfile" <<EOS
type = process
command = /bin/sleep 3600
restart = no
cgroup = $_cg
memory-pressure-watch = yes
EOS
perf_run_iters "$ITERS" "CtlStartStop_psi_memory_svc" \
    "slinitctl start $_name && slinitctl stop $_name"
slinitctl unload "$_name" > /dev/null 2>&1
rm -f "$_svcfile"
rmdir "$_cg" 2>/dev/null || true
