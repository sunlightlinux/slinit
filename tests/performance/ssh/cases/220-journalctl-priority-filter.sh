# 220-journalctl-priority-filter — priority filter cost. Compare
# unfiltered vs `-p err` (errors + higher). A tight ratio means
# filter is pushed down to scan; a wide ratio means slinit-
# journalctl decodes then discards.
perf_run_iters "$ITERS" "JournalFetchN500_nofilter" "slinit-journalctl -n 500"
perf_run_iters "$ITERS" "JournalFetchN500_p_err"    "slinit-journalctl -p err -n 500"
