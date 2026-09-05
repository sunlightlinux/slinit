# 60-parallel-status-8 — 8 parallel `slinitctl status boot` jobs
# per iteration; measures the wall-clock for all 8 to complete.
# Divide by 8 to get per-client cost under concurrent load — if
# larger than the serial `30-ctl-status` median, the control
# server is serialising or the mutex is contended.
perf_run_iters "$ITERS" "ParallelStatus8" \
    'for _n in 1 2 3 4 5 6 7 8; do slinitctl status boot > /dev/null & done; wait'
