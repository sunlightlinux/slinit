# 580-parallel-lifecycle-4 — 4 concurrent throwaway lifecycles
# (write → start → stop → unload → rm), all fired at once.
# Compare wall-clock vs `460` (serial single lifecycle). Should
# be dramatically faster than 4x serial if the lifecycle path
# is parallel-safe (writes to different files, different svc
# names in slinit's map).
#
# This case found a real one: on v2.2.6 the concurrent
# write→start→stop→unload raced `pkg/config`'s unguarded
# DirLoader map, Go's runtime killed PID 1, and the kernel
# panicked. Fixed in v2.2.7 (`cfe16ab`), and it ran gated behind
# SLINIT_ALLOW_DISRUPTIVE=1 for nine releases after the fix
# shipped — so the only integration-level guard for that panic
# never executed again. The gate is gone: it runs, and if the
# mutex is ever removed this case is what takes PID 1 down and
# says why.
_lifecycle_one() {
    _name="perf-throwaway-plc-$$-$1"
    _svcfile="/etc/slinit.d/$_name"
    printf "type = scripted\ncommand = /bin/true\n" > "$_svcfile"
    slinitctl start "$_name" > /dev/null 2>&1
    slinitctl stop  "$_name" > /dev/null 2>&1
    slinitctl unload "$_name" > /dev/null 2>&1
    rm -f "$_svcfile"
}
_par4() {
    _lifecycle_one "${1}a" &
    _lifecycle_one "${1}b" &
    _lifecycle_one "${1}c" &
    _lifecycle_one "${1}d" &
    wait
}
_iter=0
perf_run_iters "$ITERS" "ServiceLifecycle_4parallel" \
    '_iter=$((_iter+1)); _par4 $_iter'
# Belt-and-braces sweep
rm -f /etc/slinit.d/perf-throwaway-plc-$$-* 2>/dev/null
