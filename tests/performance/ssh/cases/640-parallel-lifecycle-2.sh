# 640-parallel-lifecycle-2 — 2 concurrent throwaway lifecycles.
# Bottom of the lifecycle-scaling curve. Together with 460
# (serial), 580 (4-way), 660 (16-way), and 600 (20-way) it plots
# how the fixed DirLoader mutex serialisation scales — expect
# each level to be ~= serial N × single-lifecycle-cost since
# LoadService is now fully serialised.
_lifecycle_one() {
    _name="perf-throwaway-p2-$$-$1"
    _svcfile="/etc/slinit.d/$_name"
    printf "type = scripted\ncommand = /bin/true\n" > "$_svcfile"
    slinitctl start "$_name" > /dev/null 2>&1
    slinitctl stop  "$_name" > /dev/null 2>&1
    slinitctl unload "$_name" > /dev/null 2>&1
    rm -f "$_svcfile"
}
_par2() {
    _lifecycle_one "${1}a" &
    _lifecycle_one "${1}b" &
    wait
}
_iter=0
perf_run_iters "$ITERS" "ServiceLifecycle_2parallel" \
    '_iter=$((_iter+1)); _par2 $_iter'
rm -f /etc/slinit.d/perf-throwaway-p2-$$-* 2>/dev/null
