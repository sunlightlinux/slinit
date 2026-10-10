# 80-journal-fetch-scaling — journal-read latency vs N. Reads should
# scale roughly linearly with N; a superlinear curve here points at
# decode / format / sort inefficiency on the read path.
#
# The top of the curve is bounded by the ring, not by the number asked
# for. `slinit-journalctl` reads PID 1's ring, which holds 4096 events:
# on the target, `-n 5000` returns exactly 4096 and `-n 100000` returns
# 4096 as well. So the label used to say N5000 for a read that could
# never fetch 5000, and that figure moved 3x across one day (18.842 ms,
# then 12.468, then 6.546) for reasons that are NOT ring occupancy — the
# ring was full in each case, measured. Whatever the cause, a label that
# overstates its own N makes it harder to find.
#
# The top point is therefore labelled with what comes back, discovered
# rather than hardcoded, so the label cannot go stale if the ring's size
# changes.
_ring=$(slinit-journalctl -n 1000000 2>/dev/null | wc -l)

for _n in 10 100 1000; do
    perf_run_iters "$ITERS" "JournalFetchN${_n}" "slinit-journalctl -n $_n"
done

# Ask for more than the ring can hold; report the count actually served.
perf_run_iters "$ITERS" "JournalFetchN${_ring}_ring_capped" "slinit-journalctl -n 5000"
