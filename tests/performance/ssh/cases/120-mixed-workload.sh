# 120-mixed-workload — simulates realistic operator: interleave
# status / ls / boot-time / journalctl in tight succession, 40 ops
# per iteration. Answers "what does a busy admin session cost"
# rather than the pure per-op numbers. If total wall-clock is
# markedly higher than the sum of individual medians, some slow
# path is being hit that the isolated cases miss (warmup, cache
# invalidation, GC pause).
perf_run_iters "$ITERS" "MixedOperatorWorkload_40" '
    _n=0
    while [ $_n -lt 10 ]; do
        slinitctl status boot > /dev/null
        slinitctl ls > /dev/null
        slinitctl boot-time > /dev/null
        slinit-journalctl -n 20 > /dev/null 2>&1
        _n=$((_n+1))
    done'
