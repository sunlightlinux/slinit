# 230-parallel-journalctl-8 — 8 concurrent `slinit-journalctl -n 100`.
# The reader path was proven serial-fast in `50` and concurrent-
# server-fast (via status) in `060`. This case combines both —
# does slinit-journalctl's reader also scale under concurrent
# clients, or does the on-disk file open/lock/decode serialise?
perf_run_iters "$ITERS" "ParallelJournalFetch8" \
    '_n=0; while [ $_n -lt 8 ]; do slinit-journalctl -n 100 > /dev/null 2>&1 & _n=$((_n+1)); done; wait'
