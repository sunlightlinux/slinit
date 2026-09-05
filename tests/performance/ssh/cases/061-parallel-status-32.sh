# 61-parallel-status-32 — 32 concurrent clients. Reveals whether
# per-client latency stays flat (control server scales) or blows up
# (serialisation / lock contention). Compare median to
# `60-parallel-status-8` to see if the slope is linear-in-N or
# superlinear.
perf_run_iters "$ITERS" "ParallelStatus32" \
    '_n=0; while [ $_n -lt 32 ]; do slinitctl status boot > /dev/null & _n=$((_n+1)); done; wait'
