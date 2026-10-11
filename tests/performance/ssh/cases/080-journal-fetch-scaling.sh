# 80-journal-fetch-scaling — journal-read latency vs N. Reads should
# scale roughly linearly with N; a superlinear curve here points at
# decode / format / sort inefficiency on the read path.
#
# The top of the curve is bounded by the ring, not by the number asked
# for. `slinit-journalctl` reads PID 1's ring, which holds at most 4096
# events, so `-n 5000` and `-n 100000` both return at most that many.
# The label used to say N5000 for a read that could never fetch 5000.
#
# Labelling it with the count actually served, discovered rather than
# hardcoded, immediately explained something this case had been doing
# for a day without anyone being able to say why: the top figure moved
# 3x, 18.842 then 12.468 then 6.546 ms. An earlier version of this
# comment asserted that ring occupancy was NOT the cause "since the ring
# was full in each case, measured" — which was wrong, and wrong in an
# avoidable way: the occupancy was checked at moments other than the
# runs being explained. The ring is only full on a box that has been up
# long enough to fill it.
#
# With the count in the label the relationship is plain. Measured on the
# target: 4096 entries ~18-19 ms, 1987 entries 7.4 ms, 1801 entries
# ~11 ms on a single sample. The figure tracks how full the ring is, so
# comparing it across runs means comparing the N in its own label first.
_ring=$(slinit-journalctl -n 1000000 2>/dev/null | wc -l)

for _n in 10 100 1000; do
    perf_run_iters "$ITERS" "JournalFetchN${_n}" "slinit-journalctl -n $_n"
done

# Ask for more than the ring can hold; report the count actually served.
perf_run_iters "$ITERS" "JournalFetchN${_ring}_ring_capped" "slinit-journalctl -n 5000"
