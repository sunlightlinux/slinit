# 50-journalctl-fetch-n100 — `slinit-journalctl -n 100`: read the
# last 100 records from the journal (ring buffer OR on-disk file,
# whichever is authoritative). Measures the read path through
# pkg/journal / pkg/journalbin including decode + format.
perf_run_iters "$ITERS" "JournalFetchN100" "slinit-journalctl -n 100"
