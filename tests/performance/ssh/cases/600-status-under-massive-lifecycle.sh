# 600-status-under-massive-lifecycle — 20 concurrent throwaway
# lifecycles hammering slinit's start/stop/unload paths while
# a light `slinitctl status` loop runs. Measures whether short
# reads get starved when the whole state machine is churning.
# Healthy: status p99 stays within ~2x isolated baseline.
#
# Same class of crash as `580` on v2.2.6, fanned out 5x harder,
# so it panicked faster. Fixed in v2.2.7 (`cfe16ab`) and ungated
# once both were verified on v3.1.1 — see 580's header for why
# leaving them gated after the fix was worse than running them.
_hammer_lifecycle() {
    _name="perf-throwaway-mass-$$-$1"
    _svcfile="/etc/slinit.d/$_name"
    printf "type = scripted\ncommand = /bin/true\n" > "$_svcfile"
    slinitctl start "$_name"   > /dev/null 2>&1
    slinitctl stop  "$_name"   > /dev/null 2>&1
    slinitctl unload "$_name"  > /dev/null 2>&1
    rm -f "$_svcfile"
}
_bg_hammer() {
    _i=0
    while [ $_i -lt 20 ]; do
        _hammer_lifecycle "$_i" &
        _i=$((_i + 1))
    done
    wait
}
_bg_hammer &
_bg_pid=$!

_s=$(mktemp)
_n=0
while [ $_n -lt 100 ]; do
    _t0=$(perf_now_ns); slinitctl status boot > /dev/null; _t1=$(perf_now_ns)
    echo $((_t1-_t0)) >> "$_s"; _n=$((_n+1))
done
wait "$_bg_pid" 2>/dev/null

_p50=$(sort -n "$_s"|awk 'BEGIN{c=0}{a[c++]=$1}END{print a[int(0.5*(c-1)+0.5)]}')
_p95=$(sort -n "$_s"|awk 'BEGIN{c=0}{a[c++]=$1}END{print a[int(0.95*(c-1)+0.5)]}')
_p99=$(sort -n "$_s"|awk 'BEGIN{c=0}{a[c++]=$1}END{print a[int(0.99*(c-1)+0.5)]}')
_max=$(sort -n "$_s"|tail -1); rm -f "$_s"

# Belt-and-braces cleanup
rm -f /etc/slinit.d/perf-throwaway-mass-$$-* 2>/dev/null

printf "BenchmarkStatus_UnderLifecycleStorm 100  p50=%6.3f ms  p95=%6.3f ms  p99=%6.3f ms  max=%6.3f ms\n" \
    "$(awk -v n=$_p50 'BEGIN{print n/1e6}')" \
    "$(awk -v n=$_p95 'BEGIN{print n/1e6}')" \
    "$(awk -v n=$_p99 'BEGIN{print n/1e6}')" \
    "$(awk -v n=$_max 'BEGIN{print n/1e6}')"
