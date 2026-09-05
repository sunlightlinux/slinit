# 80-journal-fetch-scaling — journal-read latency vs N (10, 100,
# 1000, 5000). Reads should scale roughly linearly with N; a
# superlinear curve here points at decode / format / sort inefficiency
# on the read path.
for _n in 10 100 1000 5000; do
    perf_run_iters "$ITERS" "JournalFetchN${_n}" "slinit-journalctl -n $_n"
done
