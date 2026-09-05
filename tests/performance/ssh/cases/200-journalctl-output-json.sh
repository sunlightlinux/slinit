# 200-journalctl-output-json — measure the cost of the `--output=json`
# format vs the default short format for the same N. Higher delta
# than the parse cost (roughly the same records read + decoded)
# points at JSON marshal being the hot path — worth switching to
# json.Encoder / precomputed field-tag tables if so.
perf_run_iters "$ITERS" "JournalFetchN500_short" "slinit-journalctl -n 500"
perf_run_iters "$ITERS" "JournalFetchN500_json"  "slinit-journalctl -n 500 --output=json"
