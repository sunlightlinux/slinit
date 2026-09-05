# 140-rss-delta-under-load — snapshot PID-1 (slinit) VmRSS +
# VmPeak + Threads BEFORE, hammer 2000 status + 500 journalctl
# calls, snapshot AFTER. Reports delta. A healthy slinit shows
# essentially zero RSS growth (Go GC releases; no goroutine leaks;
# no map growth beyond scratch). Any material increase (>1 MB or
# >5 threads) points at a leak worth `pprof`-ing.
_read_pid1() {
    awk '/^VmRSS:/ {rss=$2} /^VmPeak:/ {peak=$2} /^Threads:/ {thr=$2} \
         END {printf "%d %d %d\n", rss, peak, thr}' /proc/1/status
}

_before=$(_read_pid1)
_rss_before=$(echo "$_before" | awk '{print $1}')
_peak_before=$(echo "$_before" | awk '{print $2}')
_thr_before=$(echo "$_before" | awk '{print $3}')

echo "  before: RSS=${_rss_before}kB  VmPeak=${_peak_before}kB  threads=${_thr_before}"

_i=0
while [ $_i -lt 2000 ]; do
    slinitctl status boot > /dev/null
    _i=$((_i + 1))
done

_i=0
while [ $_i -lt 500 ]; do
    slinit-journalctl -n 50 > /dev/null 2>&1
    _i=$((_i + 1))
done

# Give the GC one hint to sweep before the snapshot. slinit has no
# manual GC trigger via slinitctl; a version query is a cheap
# no-op that involves a request/response cycle giving the runtime
# time to run background collection.
slinitctl --version > /dev/null
sleep 1

_after=$(_read_pid1)
_rss_after=$(echo "$_after" | awk '{print $1}')
_peak_after=$(echo "$_after" | awk '{print $2}')
_thr_after=$(echo "$_after" | awk '{print $3}')

_rss_delta=$(( _rss_after - _rss_before ))
_peak_delta=$(( _peak_after - _peak_before ))
_thr_delta=$(( _thr_after - _thr_before ))

echo "  after : RSS=${_rss_after}kB  VmPeak=${_peak_after}kB  threads=${_thr_after}"
printf "BenchmarkPID1_RSS_Delta_2500ops     1  delta=%+5d kB  peak_delta=%+5d kB  thread_delta=%+d\n" \
    "$_rss_delta" "$_peak_delta" "$_thr_delta"
