# 160-journal-scan-vs-size — intended to measure whether journal fetch
# cost grows with TOTAL journal size rather than with the requested N:
# fetch a small N, emit 5000 entries, fetch the same N again. A
# materially slower second fetch would mean the reader scans the whole
# journal instead of seeking to the tail.
#
# READ THIS BEFORE TRUSTING THE FIGURE — twice over.
#
# First, the case cannot observe what it varies. `slinit-journalctl`
# reads PID 1's ring, and that ring is capped: asking for 100000 entries
# on the target returns exactly 4096, and it is already full on any box
# that has been up for a while. Emitting 5000 more entries therefore
# REPLACES ring contents instead of growing anything, so total size is
# constant across the two measurements.
#
# Second, the delta is dominated by when each arm ran. Across five runs
# it read -17%, -14%, -23%, -36% and then +51%: a swing of nearly 90
# points, crossing zero, on a quantity that cannot have changed. An
# earlier version of this comment called the measurement "stable enough
# to mean something" after warm-up and more samples; the +51% disproved
# that. The pump is 5000 `logger` invocations, so the "after" arm runs
# in the wake of 5000 process spawns, and that — not journal size — is
# what the delta mostly reports.
#
# So the baseline is bracketed, as in `750` and `790`: measured once
# before the pump and once after, with the two printed along with their
# disagreement. When the brackets differ by more than the delta, the
# delta means nothing and the line says so without anyone having to
# work it out.
#
# None of this fixes the premise. A real size-scaling test has to read
# the on-disk journal that slinit-journald writes, where the file does
# grow, rather than a ring that cannot.

_fetch="slinit-journalctl -n 100"

_b1_med=$(perf_median_ns 9 "$_fetch")

# Emit 5000 entries into a distinct tag
_pump_tag="perf_pump_$$"
_i=0
while [ $_i -lt 5000 ]; do
    logger -t "$_pump_tag" "pump-$_i"
    _i=$((_i+1))
done
sleep 0.5

# The post-pump arm is measured twice as well: a minimum on one side
# against a single reading on the other biases the delta, which is why
# this case printed +1% and +2% where the symmetric form reads ~0%.
_after1_med=$(perf_median_ns 9 "$_fetch")
_after2_med=$(perf_median_ns 9 "$_fetch")
_after_med=$(perf_baseline_ns "$_after1_med" "$_after2_med")

# Second bracket. The pump's own aftermath is what the first version of
# this case mistook for a size effect, so this reading is taken once the
# spawning has stopped and the daemon has had a moment.
sleep 1
_b2_med=$(perf_median_ns 9 "$_fetch")

_base_med=$(perf_baseline_ns "$_b1_med" "$_b2_med")
_delta=$(awk -v a="$_after_med" -v b="$_base_med" 'BEGIN{print a-b}')
_pct=$(awk -v b="$_base_med" -v d="$_delta" 'BEGIN{if(b>0) printf "%+.0f", 100*d/b; else print "n/a"}')
_spread=$(awk -v a="$_b1_med" -v b="$_b2_med" 'BEGIN{
    m=(a+b)/2; d=a-b; if(d<0) d=-d; if(m>0) printf "%.0f", 100*d/m; else print "n/a"}')

printf "BenchmarkJournalFetchN100_scan    27  base=%6.3f ms (%6.3f/%6.3f, spread %s%%)  after_5k_pump=%6.3f ms  delta=%+6.3f ms (%s%%)\n" \
    "$(awk -v n="$_base_med" 'BEGIN{print n/1e6}')" \
    "$(awk -v n="$_b1_med" 'BEGIN{print n/1e6}')" \
    "$(awk -v n="$_b2_med" 'BEGIN{print n/1e6}')" \
    "$_spread" \
    "$(awk -v n="$_after_med" 'BEGIN{print n/1e6}')" \
    "$(awk -v n="$_delta" 'BEGIN{print n/1e6}')" \
    "$_pct"
