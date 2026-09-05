# 062-parallel-status-128 — 128 concurrent clients. If per-client
# latency (wall / 128) stays comparable to the 32-client number
# from `061`, slinit's control server scales cleanly through
# operator-realistic fan-out. A knee here (say >2x per-client at
# 128 vs 32) points at a mutex hot-path, accept-loop backlog, or
# fd-limit throttle worth investigating.
perf_run_iters "$ITERS" "ParallelStatus128" \
    '_n=0; while [ $_n -lt 128 ]; do slinitctl status boot > /dev/null & _n=$((_n+1)); done; wait'
