# 250-fair-share-latency — light client's per-op p99 while a
# heavy load runs in the background. Simulates "monitoring script
# hits `slinitctl status` once/sec while an admin runs `journalctl
# -n 10000`". Fair scheduler + per-connection state should keep
# the light client's p99 close to the isolated baseline (~1 ms).
# A visible degradation means slinit's scheduler is starving the
# short queries under a big-request work-load.
#
# Background: emit 4 heavy fetches (5000 records) sequentially.
# Foreground: 100 status calls, record each latency, take p99.
_heavy_bg() {
    _n=0
    while [ $_n -lt 4 ]; do
        slinit-journalctl -n 5000 > /dev/null 2>&1
        _n=$((_n + 1))
    done
}
_heavy_bg &
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
printf "BenchmarkStatus_UnderHeavyRead    100  p50=%6.3f ms  p95=%6.3f ms  p99=%6.3f ms  max=%6.3f ms\n" \
    "$(awk -v n=$_p50 'BEGIN{print n/1e6}')" \
    "$(awk -v n=$_p95 'BEGIN{print n/1e6}')" \
    "$(awk -v n=$_p99 'BEGIN{print n/1e6}')" \
    "$(awk -v n=$_max 'BEGIN{print n/1e6}')"
