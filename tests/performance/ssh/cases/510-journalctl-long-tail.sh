# 510-journalctl-long-tail — 300x `slinit-journalctl -n 100`,
# p50/p95/p99/max. Completes the long-tail set (status→150,
# ls→170, boot-time→180, journalctl→here). The journal-read path
# spans more code (decode+format+I/O) than the pure IPC paths;
# a wider p99/median ratio here vs other cases points at
# decode-side jitter — file cache, sort/aggregate, or GC on the
# response buffer.
_s=$(mktemp)
_n=0
while [ $_n -lt 300 ]; do
    _t0=$(perf_now_ns); slinit-journalctl -n 100 > /dev/null 2>&1; _t1=$(perf_now_ns)
    echo $((_t1-_t0)) >> "$_s"; _n=$((_n+1))
done
_p50=$(sort -n "$_s"|awk 'BEGIN{c=0}{a[c++]=$1}END{print a[int(0.5*(c-1)+0.5)]}')
_p95=$(sort -n "$_s"|awk 'BEGIN{c=0}{a[c++]=$1}END{print a[int(0.95*(c-1)+0.5)]}')
_p99=$(sort -n "$_s"|awk 'BEGIN{c=0}{a[c++]=$1}END{print a[int(0.99*(c-1)+0.5)]}')
_max=$(sort -n "$_s"|tail -1); rm -f "$_s"
printf "BenchmarkJournalFetchN100_LongTail 300 p50=%6.3f ms  p95=%6.3f ms  p99=%6.3f ms  max=%6.3f ms\n" \
    "$(awk -v n=$_p50 'BEGIN{print n/1e6}')" \
    "$(awk -v n=$_p95 'BEGIN{print n/1e6}')" \
    "$(awk -v n=$_p99 'BEGIN{print n/1e6}')" \
    "$(awk -v n=$_max 'BEGIN{print n/1e6}')"
