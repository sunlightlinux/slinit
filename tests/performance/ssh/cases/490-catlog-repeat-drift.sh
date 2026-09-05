# 490-catlog-repeat-drift — 10 batches × 50 catlog calls on
# socklog. Drift on the log-read path would show as monotonic
# increase (buffer growth un-managed) or spikes (buffer rollover).
# Complements `130`/`260` drift detectors on the read side.
#
# NOTE: catlog only works against services configured with
# `log-type = buffer` or `log-file = /path`. If the target
# service doesn't carry either, the CLI returns an error immediately
# and this case is measuring the error-return path, not the log-
# read path. Detect + skip so the result isn't misinterpreted.
if ! slinitctl catlog socklog > /dev/null 2>&1; then
    echo "SKIP: 'socklog' has no log-type=buffer / log-file — catlog unsupported"
    return 0 2>/dev/null || exit 0
fi
_bat=0
while [ $_bat -lt 10 ]; do
    _bat=$((_bat + 1))
    _t0=$(perf_now_ns)
    _n=0
    while [ $_n -lt 50 ]; do
        slinitctl catlog socklog > /dev/null
        _n=$((_n + 1))
    done
    _t1=$(perf_now_ns)
    _dur=$(( _t1 - _t0 ))
    printf "  batch %2d/10  50 catlog ops  wall=%.1f ms  per-op=%.3f ms\n" \
        "$_bat" \
        "$(awk -v n="$_dur" 'BEGIN{printf "%.1f", n/1e6}')" \
        "$(awk -v n="$_dur" 'BEGIN{printf "%.3f", n/1e6/50}')"
done
