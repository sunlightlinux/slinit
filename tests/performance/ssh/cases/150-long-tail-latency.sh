# 150-long-tail-latency — 500 sequential status ops; report p50,
# p95, p99, MAX. Wide-percentile view catches long-tail pauses
# (GC stop-the-world, scheduler jitter, disk-IO on syscall) that
# median hides. Healthy slinit: p99 within ~2x p50; MAX under
# ~10ms. Large gap = worth investigating.
_samples="$(mktemp)"
_n=0
while [ $_n -lt 500 ]; do
    _t0=$(perf_now_ns)
    slinitctl status boot > /dev/null
    _t1=$(perf_now_ns)
    echo $(( _t1 - _t0 )) >> "$_samples"
    _n=$((_n + 1))
done

_p50=$(sort -n "$_samples" | awk 'BEGIN{c=0} {a[c++]=$1} END{print a[int(0.5*(c-1)+0.5)]}')
_p95=$(sort -n "$_samples" | awk 'BEGIN{c=0} {a[c++]=$1} END{print a[int(0.95*(c-1)+0.5)]}')
_p99=$(sort -n "$_samples" | awk 'BEGIN{c=0} {a[c++]=$1} END{print a[int(0.99*(c-1)+0.5)]}')
_max=$(sort -n "$_samples" | tail -1)
rm -f "$_samples"

printf "BenchmarkStatus_LongTail_500      500  p50=%6.3f ms  p95=%6.3f ms  p99=%6.3f ms  max=%6.3f ms\n" \
    "$(awk -v n="$_p50" 'BEGIN{print n/1e6}')" \
    "$(awk -v n="$_p95" 'BEGIN{print n/1e6}')" \
    "$(awk -v n="$_p99" 'BEGIN{print n/1e6}')" \
    "$(awk -v n="$_max" 'BEGIN{print n/1e6}')"
