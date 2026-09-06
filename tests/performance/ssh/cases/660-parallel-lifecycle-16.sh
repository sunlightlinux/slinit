# 660-parallel-lifecycle-16 — 16 concurrent throwaway lifecycles.
# Between 8-way (650) and 20-way (600). Together they plot the
# post-fix scaling curve at 2/4/8/16/20-way fan-out. Would
# absolutely have crashed slinit pre-fix.
_lifecycle_one() {
    _name="perf-throwaway-p16-$$-$1"
    _svcfile="/etc/slinit.d/$_name"
    printf "type = scripted\ncommand = /bin/true\n" > "$_svcfile"
    slinitctl start "$_name" > /dev/null 2>&1
    slinitctl stop  "$_name" > /dev/null 2>&1
    slinitctl unload "$_name" > /dev/null 2>&1
    rm -f "$_svcfile"
}
_par16() {
    _i=0
    while [ $_i -lt 16 ]; do
        _lifecycle_one "${1}_$_i" &
        _i=$((_i + 1))
    done
    wait
}
_iter=0
perf_run_iters "$ITERS" "ServiceLifecycle_16parallel" \
    '_iter=$((_iter+1)); _par16 $_iter'
rm -f /etc/slinit.d/perf-throwaway-p16-$$-* 2>/dev/null
