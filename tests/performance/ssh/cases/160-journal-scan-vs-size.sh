# 160-journal-scan-vs-size — measure whether journal fetch cost
# grows with TOTAL journal size (not just requested N). Baseline
# fetch, emit 5000 log entries, re-fetch same small N. If the
# second fetch is materially slower, slinit is scanning the whole
# journal on every read rather than seeking to the tail — a
# recoverable design flaw the reader path might have.
_baseline_ns() {
    _t0=$(perf_now_ns)
    slinit-journalctl -n 100 > /dev/null 2>&1
    _t1=$(perf_now_ns)
    echo $(( _t1 - _t0 ))
}

# Median of 5 baseline fetches
_i=0; _samples="$(mktemp)"
while [ $_i -lt 5 ]; do _baseline_ns >> "$_samples"; _i=$((_i+1)); done
_base_med=$(sort -n "$_samples" | awk 'BEGIN{c=0} {a[c++]=$1} END{
    if (c%2) print a[int(c/2)]; else print (a[c/2-1]+a[c/2])/2}')
rm -f "$_samples"

# Emit 5000 entries into a distinct tag
_pump_tag="perf_pump_$$"
_i=0
while [ $_i -lt 5000 ]; do
    logger -t "$_pump_tag" "pump-$_i"
    _i=$((_i+1))
done
sleep 0.5

# 5 more baseline fetches (same n=100), median
_i=0; _samples="$(mktemp)"
while [ $_i -lt 5 ]; do _baseline_ns >> "$_samples"; _i=$((_i+1)); done
_after_med=$(sort -n "$_samples" | awk 'BEGIN{c=0} {a[c++]=$1} END{
    if (c%2) print a[int(c/2)]; else print (a[c/2-1]+a[c/2])/2}')
rm -f "$_samples"

_delta=$(( _after_med - _base_med ))
_pct=$(awk -v b="$_base_med" -v d="$_delta" 'BEGIN{if(b>0) printf "%+.0f", 100*d/b; else print "n/a"}')

printf "BenchmarkJournalFetchN100_scan     2  before=%6.3f ms  after_5k_pump=%6.3f ms  delta=%+6.3f ms (%s%%)\n" \
    "$(awk -v n="$_base_med" 'BEGIN{print n/1e6}')" \
    "$(awk -v n="$_after_med" 'BEGIN{print n/1e6}')" \
    "$(awk -v n="$_delta" 'BEGIN{print n/1e6}')" \
    "$_pct"
