# 170-ls-long-tail — 500x `slinitctl ls`, p50/p95/p99/max. Different
# code path than status (full enumeration vs single-service query);
# a divergence in tail shape vs `150-long-tail` reveals per-op
# scaling that scales-with-N-services rather than per-service work.
_s=$(mktemp)
_n=0
while [ $_n -lt 500 ]; do
    _t0=$(perf_now_ns); slinitctl ls > /dev/null; _t1=$(perf_now_ns)
    echo $((_t1-_t0)) >> "$_s"; _n=$((_n+1))
done
_p50=$(sort -n "$_s"|awk 'BEGIN{c=0}{a[c++]=$1}END{print a[int(0.5*(c-1)+0.5)]}')
_p95=$(sort -n "$_s"|awk 'BEGIN{c=0}{a[c++]=$1}END{print a[int(0.95*(c-1)+0.5)]}')
_p99=$(sort -n "$_s"|awk 'BEGIN{c=0}{a[c++]=$1}END{print a[int(0.99*(c-1)+0.5)]}')
_max=$(sort -n "$_s"|tail -1); rm -f "$_s"
printf "BenchmarkLs_LongTail_500          500  p50=%6.3f ms  p95=%6.3f ms  p99=%6.3f ms  max=%6.3f ms\n" \
    "$(awk -v n=$_p50 'BEGIN{print n/1e6}')" \
    "$(awk -v n=$_p95 'BEGIN{print n/1e6}')" \
    "$(awk -v n=$_p99 'BEGIN{print n/1e6}')" \
    "$(awk -v n=$_max 'BEGIN{print n/1e6}')"
