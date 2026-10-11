# 140-rss-delta-under-load — snapshot PID-1 (slinit) VmRSS + VmPeak +
# Threads BEFORE, hammer 2000 status + 500 journalctl calls, snapshot
# AFTER.
#
# WHAT THIS CASE CAN AND CANNOT DETECT. Its RSS delta has been measured
# at +4, +120, +176, +1412, +1576, -72, +352, +712, +748, +912 and
# +1612 kB. Two explanations were tested and neither holds:
#
#   * Sampling noise. PID 1's VmRSS is *exactly* constant at rest —
#     fifteen reads over thirty seconds on the target gave the same
#     number — and a median of five reads 200ms apart duly reported a
#     spread of 0%, costing two seconds a side and buying nothing. So
#     the reads are not noisy; the sampler was removed again.
#   * An unsettled daemon. One +2184 kB run was indeed a target a
#     minute into its uptime, with thread_delta +4 and peak_delta
#     +295 MB beside it. But -72, +352 and +1612 kB all came from a
#     settled daemon with both those deltas at zero, so settledness
#     does not explain the range either.
#
# What is left is where the Go GC happens to leave the heap after a
# burst of 2500 requests, which is a real quantity and not a stable
# one. So this delta cannot resolve a leak smaller than about 2 MB, and
# a single large reading is not evidence of one. peak_delta and
# thread_delta ARE worth reading — nonzero means the daemon was still
# growing — and the daemon's uptime is printed so that is checkable
# rather than inferred.
#
# The leak question is answered by `141` instead, and by its shape
# rather than any single delta: a per-op leak keeps rising with the op
# count. Its curve moves in both directions at every checkpoint, which
# is one-time arena growth.
_read_pid1() {
    awk '/^VmRSS:/ {rss=$2} /^VmPeak:/ {peak=$2} /^Threads:/ {thr=$2} \
         END {printf "%d %d %d\n", rss, peak, thr}' /proc/1/status
}

_before=$(_read_pid1)
_peak_before=$(echo "$_before" | awk '{print $2}')
_thr_before=$(echo "$_before" | awk '{print $3}')
_rss_before=$(echo "$_before" | awk '{print $1}')
_pid1_age=$(awk '{printf "%d", $1}' /proc/uptime)

echo "  before: RSS=${_rss_before}kB  VmPeak=${_peak_before}kB" \
     " threads=${_thr_before}  (uptime ${_pid1_age}s)"

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
_peak_after=$(echo "$_after" | awk '{print $2}')
_thr_after=$(echo "$_after" | awk '{print $3}')
_rss_after=$(echo "$_after" | awk '{print $1}')

_rss_delta=$(( _rss_after - _rss_before ))
_peak_delta=$(( _peak_after - _peak_before ))
_thr_delta=$(( _thr_after - _thr_before ))

echo "  after : RSS=${_rss_after}kB  VmPeak=${_peak_after}kB  threads=${_thr_after}"
# peak_delta and thread_delta are the settledness tells: when either is
# nonzero the daemon was still growing and the RSS delta is not
# attributable to the load.
printf "BenchmarkPID1_RSS_Delta_2500ops     1  delta=%+5d kB (±2 MB, see case notes)  peak_delta=%+6d kB  thread_delta=%+d  (pid1 uptime %ss)\n" \
    "$_rss_delta" "$_peak_delta" "$_thr_delta" "$_pid1_age"
