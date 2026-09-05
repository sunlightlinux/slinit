# remote-prelude.sh — sourced on the target BEFORE each perf case.
# Provides timing + statistics + summary helpers. POSIX sh compatible
# so it runs on busybox `sh` targets too (no arrays).
#
# All internal variables use the `_prf_` prefix to keep them out of
# any case body's namespace. POSIX sh has no `local` keyword, so a
# case that used a common name like `_i` inside a helper function
# would silently clobber our loop counter — a bug we hit and don't
# want to hit again.

# perf_now_ns — nanosecond wall clock (GNU/busybox 1.34+ `date`).
perf_now_ns() {
    date +%s%N
}

# perf_run_iters ITERS LABEL "command..."
#   Runs the command ITERS times, timing each with perf_now_ns.
#   Silences stdout/stderr so the timing is not swamped by tty flush.
#   Records deltas to a scratch file, prints benchstat-style summary
#   line: `Benchmark<LABEL>  N  median=X ms  p95=Y ms  min=Z ms`.
#   Set PERF_TRACE=1 to dump every sample to stderr for debugging.
perf_run_iters() {
    _prf_iters="$1"; _prf_label="$2"; _prf_cmd="$3"
    _prf_samples="$(mktemp)"
    _prf_i=0
    while [ "$_prf_i" -lt "$_prf_iters" ]; do
        _prf_t0="$(perf_now_ns)"
        eval "$_prf_cmd" > /dev/null 2>&1
        _prf_t1="$(perf_now_ns)"
        _prf_d=$(( _prf_t1 - _prf_t0 ))
        echo "$_prf_d" >> "$_prf_samples"
        [ -n "${PERF_TRACE:-}" ] && \
            echo "  [trace $_prf_label iter $_prf_i] delta=${_prf_d}ns" >&2
        _prf_i=$(( _prf_i + 1 ))
    done
    [ -n "${PERF_TRACE:-}" ] && {
        echo "  [trace $_prf_label] samples file:" >&2
        cat "$_prf_samples" >&2
    }
    _prf_med="$(sort -n "$_prf_samples" | awk 'BEGIN{c=0} {a[c++]=$1} END{
        if (c%2) print a[int(c/2)]; else print (a[c/2-1]+a[c/2])/2}')"
    _prf_p95="$(sort -n "$_prf_samples" | awk 'BEGIN{c=0} {a[c++]=$1} END{
        i=int(0.95*(c-1)+0.5); print a[i]}')"
    _prf_min="$(sort -n "$_prf_samples" | head -1)"
    rm -f "$_prf_samples"
    _prf_med_ms="$(awk -v n="$_prf_med" 'BEGIN{printf "%.3f", n/1e6}')"
    _prf_p95_ms="$(awk -v n="$_prf_p95" 'BEGIN{printf "%.3f", n/1e6}')"
    _prf_min_ms="$(awk -v n="$_prf_min" 'BEGIN{printf "%.3f", n/1e6}')"
    printf "Benchmark%-24s %4d  median=%8s ms  p95=%8s ms  min=%8s ms\n" \
        "$_prf_label" "$_prf_iters" "$_prf_med_ms" "$_prf_p95_ms" "$_prf_min_ms"
}
