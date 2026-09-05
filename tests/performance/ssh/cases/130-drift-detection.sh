# 130-drift-detection — 10 batches of 50 sequential status calls;
# print median per batch. Reveals per-batch drift over time. A
# healthy slinit should show flat medians across batches; an
# ascending trend points at a leak, cache pollution, or lock-hold
# growth.
#
# Uses a two-timer strategy: outer perf_run_iters times each BATCH
# of 50; inner batches print their own trend after the run
# completes.
_bat=0
while [ $_bat -lt 10 ]; do
    _bat=$((_bat + 1))
    _t0=$(perf_now_ns)
    _n=0
    while [ $_n -lt 50 ]; do
        slinitctl status boot > /dev/null
        _n=$((_n + 1))
    done
    _t1=$(perf_now_ns)
    _dur=$(( _t1 - _t0 ))
    printf "  batch %2d/10  50 status ops  wall=%.1f ms  per-op=%.3f ms\n" \
        "$_bat" \
        "$(awk -v n="$_dur" 'BEGIN{printf "%.1f", n/1e6}')" \
        "$(awk -v n="$_dur" 'BEGIN{printf "%.3f", n/1e6/50}')"
done
