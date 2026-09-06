# 860-memory-pressure-watch — startup cost of a svc that arms a
# PSI memory-pressure watcher. slinit hooks into /proc/pressure/
# memory + poll(2)s on it. Delta vs plain scripted-start (case
# 740 = 1.126 ms) = PSI watcher setup overhead.
_name="perf-psi-mem-$$"
_svcfile="/etc/slinit.d/$_name"
cat > "$_svcfile" <<EOF
type = scripted
command = /bin/true
memory-pressure-watch = yes
EOF
slinitctl load "$_name" > /dev/null 2>&1 || true

perf_run_iters "$ITERS" "CtlStart_psi_memory_svc" "slinitctl start $_name"

slinitctl unload "$_name" > /dev/null 2>&1
rm -f "$_svcfile"
