# 650-parallel-lifecycle-8 — 8 concurrent throwaway lifecycles.
# Middle of the scaling curve between 4-way (580) and 20-way (600).
# Would have hit the DirLoader race pre-fix; validates the mutex-
# serialised path holds under moderate fan-out.
_lifecycle_one() {
    _name="perf-throwaway-p8-$$-$1"
    _svcfile="/etc/slinit.d/$_name"
    printf "type = scripted\ncommand = /bin/true\n" > "$_svcfile"
    slinitctl start "$_name" > /dev/null 2>&1
    slinitctl stop  "$_name" > /dev/null 2>&1
    slinitctl unload "$_name" > /dev/null 2>&1
    rm -f "$_svcfile"
}
_par8() {
    _i=0
    while [ $_i -lt 8 ]; do
        _lifecycle_one "${1}_$_i" &
        _i=$((_i + 1))
    done
    wait
}
_iter=0
perf_run_iters "$ITERS" "ServiceLifecycle_8parallel" \
    '_iter=$((_iter+1)); _par8 $_iter'
rm -f /etc/slinit.d/perf-throwaway-p8-$$-* 2>/dev/null
