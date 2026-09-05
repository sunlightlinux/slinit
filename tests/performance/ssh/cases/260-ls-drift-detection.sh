# 260-ls-drift-detection — same shape as `130` but for `slinitctl
# ls` (enumeration path). If drift appears here but not in status
# (`130`), the culprit is in the enumeration/list-copy path
# specifically.
_bat=0
while [ $_bat -lt 10 ]; do
    _bat=$((_bat + 1))
    _t0=$(perf_now_ns)
    _n=0
    while [ $_n -lt 50 ]; do
        slinitctl ls > /dev/null
        _n=$((_n + 1))
    done
    _t1=$(perf_now_ns)
    _dur=$(( _t1 - _t0 ))
    printf "  batch %2d/10  50 ls ops  wall=%.1f ms  per-op=%.3f ms\n" \
        "$_bat" \
        "$(awk -v n="$_dur" 'BEGIN{printf "%.1f", n/1e6}')" \
        "$(awk -v n="$_dur" 'BEGIN{printf "%.3f", n/1e6/50}')"
done
