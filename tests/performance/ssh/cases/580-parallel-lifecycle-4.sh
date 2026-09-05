# 580-parallel-lifecycle-4 — 4 concurrent throwaway lifecycles
# (write → start → stop → unload → rm), all fired at once.
# Compare wall-clock vs `460` (serial single lifecycle). Should
# be dramatically faster than 4x serial if the lifecycle path
# is parallel-safe (writes to different files, different svc
# names in slinit's map).
#
# DISRUPTIVE: crashed slinit as PID 1 on ceres (v2.2.6) —
# concurrent write→start→stop→unload of throwaway svcs hits a
# state-machine race that panics the kernel. Gated behind
# SLINIT_ALLOW_DISRUPTIVE=1; kept in-tree so it can validate
# the fix. Rebuild the ISO with the fix before rerunning.
if [ "${SLINIT_ALLOW_DISRUPTIVE:-0}" != "1" ]; then
    echo "SKIP: known to crash slinit PID 1 (v2.2.6); set SLINIT_ALLOW_DISRUPTIVE=1 to run"
    return 0 2>/dev/null || exit 0
fi
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
