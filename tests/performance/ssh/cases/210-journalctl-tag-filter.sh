# 210-journalctl-tag-filter — tag-filtered fetch vs unfiltered.
# Seeds a small tagged burst first, then measures fetch(-t tag)
# vs unfiltered fetch of the same N. Filtered read should be at
# most a few percent slower than unfiltered — if it's much
# slower, the tag filter is applied AFTER decode instead of at
# scan time.
_tag="perftag_$$"
_n=0
while [ $_n -lt 100 ]; do
    logger -t "$_tag" "seed-$_n"
    _n=$((_n + 1))
done
sleep 0.2

perf_run_iters "$ITERS" "JournalFetchN100_unfiltered" "slinit-journalctl -n 100"
perf_run_iters "$ITERS" "JournalFetchN100_taggrep"    "slinit-journalctl -t $_tag -n 100"
