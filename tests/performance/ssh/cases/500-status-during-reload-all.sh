# 500-status-during-reload-all — light `slinitctl status` while
# `reload-all` hammers the server in the background. reload-all
# takes the loader mutex to re-parse each service's config; if
# that mutex is held across the whole batch (not per-service),
# status calls should stall. Healthy slinit: light-client p99
# stays close to the isolated baseline.
_reload_hammer() {
    _n=0
    while [ $_n -lt 20 ]; do
        slinitctl reload-all > /dev/null 2>&1
        _n=$((_n + 1))
    done
}
_reload_hammer &
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
printf "BenchmarkStatus_UnderReloadAll    100  p50=%6.3f ms  p95=%6.3f ms  p99=%6.3f ms  max=%6.3f ms\n" \
    "$(awk -v n=$_p50 'BEGIN{print n/1e6}')" \
    "$(awk -v n=$_p95 'BEGIN{print n/1e6}')" \
    "$(awk -v n=$_p99 'BEGIN{print n/1e6}')" \
    "$(awk -v n=$_max 'BEGIN{print n/1e6}')"
