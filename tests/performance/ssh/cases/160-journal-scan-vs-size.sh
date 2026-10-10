# 160-journal-scan-vs-size — intended to measure whether journal fetch
# cost grows with TOTAL journal size rather than with the requested N:
# fetch a small N, emit 5000 entries, fetch the same N again. A
# materially slower second fetch would mean the reader scans the whole
# journal instead of seeking to the tail.
#
# READ THIS BEFORE TRUSTING THE FIGURE. The case cannot currently
# observe what it varies. `slinit-journalctl` reads PID 1's ring, and
# that ring is capped: asking for 100000 entries on the target returns
# exactly 4096, and it is already full on any box that has been up for a
# while. Emitting 5000 more entries therefore REPLACES ring contents
# instead of growing anything, so total size is constant across the two
# measurements and no growth can show up in the delta.
#
# That is also why the delta comes out NEGATIVE — -17% before this case
# was touched and -14% after adding warm-up and tripling the samples, so
# the warm-up theory for it was wrong. Entry length is identical either
# side (87 bytes, measured), so the remaining difference is some property
# of the ring's contents and not of its size.
#
# What is fixed here is only the measurement: both medians are now of
# nine warmed-up reads rather than five possibly-cold ones, so the figure
# is stable enough to mean something. What is NOT fixed is the premise. A
# real size-scaling test has to read the on-disk journal that
# slinit-journald writes, where the file does grow, rather than a ring
# that cannot.

_fetch="slinit-journalctl -n 100"

_base_med=$(perf_median_ns 9 "$_fetch")

# Emit 5000 entries into a distinct tag
_pump_tag="perf_pump_$$"
_i=0
while [ $_i -lt 5000 ]; do
    logger -t "$_pump_tag" "pump-$_i"
    _i=$((_i+1))
done
sleep 0.5

_after_med=$(perf_median_ns 9 "$_fetch")

_delta=$(( _after_med - _base_med ))
_pct=$(awk -v b="$_base_med" -v d="$_delta" 'BEGIN{if(b>0) printf "%+.0f", 100*d/b; else print "n/a"}')

printf "BenchmarkJournalFetchN100_scan    18  before=%6.3f ms  after_5k_pump=%6.3f ms  delta=%+6.3f ms (%s%%)\n" \
    "$(awk -v n="$_base_med" 'BEGIN{print n/1e6}')" \
    "$(awk -v n="$_after_med" 'BEGIN{print n/1e6}')" \
    "$(awk -v n="$_delta" 'BEGIN{print n/1e6}')" \
    "$_pct"
