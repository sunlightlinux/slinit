# 620-journalctl-reverse — `-r` (newest first) vs default order.
# Same records, opposite direction. If reverse costs materially
# more than forward, the reader is reading forward then reversing
# in memory instead of seeking to tail + walking backward.
perf_run_iters "$ITERS" "JournalFetchN500_forward" "slinit-journalctl -n 500"
perf_run_iters "$ITERS" "JournalFetchN500_reverse" "slinit-journalctl -n 500 -r"
