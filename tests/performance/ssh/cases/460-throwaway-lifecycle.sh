# 460-throwaway-lifecycle — provision + start + stop + unload +
# remove a throwaway `type=scripted, command=/bin/true` service,
# per iteration. Measures the FULL service lifecycle path from
# disk-write to slinit-forgotten. Unique per-iteration name so
# concurrent iters don't collide. Trap-cleaned on the last
# iter's unload; if that fails, a stale file may linger under
# /etc/slinit.d/ — check with `ls /etc/slinit.d/perf-throwaway-*`
# and remove by hand.
_lifecycle() {
    _name="perf-throwaway-$$-$1"
    _svcfile="/etc/slinit.d/$_name"
    printf "type = scripted\ncommand = /bin/true\n" > "$_svcfile"
    slinitctl start "$_name" > /dev/null 2>&1
    slinitctl stop "$_name" > /dev/null 2>&1
    slinitctl unload "$_name" > /dev/null 2>&1
    rm -f "$_svcfile"
}
_iter=0
perf_run_iters "$ITERS" "ServiceLifecycle_full" \
    '_iter=$((_iter+1)); _lifecycle $_iter'
# Belt-and-braces: sweep any leftover
rm -f /etc/slinit.d/perf-throwaway-$$-* 2>/dev/null
