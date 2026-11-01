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

# perf_median_of_file FILE
#   Median of the newline-separated ns samples in FILE, on stdout. One
#   implementation, because three copies of this awk had already appeared
#   and a median that disagrees with itself between cases is worse than no
#   median at all.
perf_median_of_file() {
    sort -n "$1" | awk 'BEGIN{c=0} {a[c++]=$1} END{
        if (c%2) print a[int(c/2)]; else print (a[c/2-1]+a[c/2])/2}'
}

# perf_median_ns ITERS "command..."
#   Median in ns on stdout, with no benchmark line printed — for cases
#   that compare two medians and report a ratio rather than one figure.
#
#   It warms up first, so a cold "before" is not compared against a warm
#   "after". 790 needed that and the sample count: it timed ONE `list`
#   each side and reported 1.06x on one run and 0.87x on the next, where
#   0.87x says adding 100 services made `list` faster. With medians of
#   warmed runs it reads 1.00x.
#
#   Warm-up was NOT the explanation for 160's negative delta, which
#   survived it almost unchanged (-17% to -14%). See that case: its ring
#   is capped, so the size it claims to vary never changes.
perf_median_ns() {
    _pmn_iters="$1"; _pmn_cmd="$2"
    _pmn_w=0
    while [ "$_pmn_w" -lt 3 ]; do
        eval "$_pmn_cmd" > /dev/null 2>&1
        _pmn_w=$(( _pmn_w + 1 ))
    done
    _pmn_f="$(mktemp)"
    _pmn_i=0
    while [ "$_pmn_i" -lt "$_pmn_iters" ]; do
        _pmn_t0="$(perf_now_ns)"
        eval "$_pmn_cmd" > /dev/null 2>&1 || _prf_note_failure "$_pmn_cmd"
        _pmn_t1="$(perf_now_ns)"
        echo $(( _pmn_t1 - _pmn_t0 )) >> "$_pmn_f"
        _pmn_i=$(( _pmn_i + 1 ))
    done
    perf_median_of_file "$_pmn_f"
    rm -f "$_pmn_f"
}

# perf_baseline_ns A B
#   The baseline to compare a bracketed measurement against, given the
#   two bracket readings in ns. It returns the SMALLER one, not their
#   mean, and the reason is the shape of the distribution rather than a
#   preference.
#
#   Latency has a hard floor — the work cannot take less than it takes —
#   so contamination is one-sided: an interrupted sample is always slow,
#   never fast. Measured on the target, a ~1.7 ms journal read produces
#   occasional 2.2-2.9 ms brackets, and across six runs the outlier
#   landed on the first reading as often as the second, so it is not
#   something an ordering change can avoid. A mean splits the difference
#   with a contaminant and drags the baseline up, which is what made a
#   delta of zero print as -18%. The minimum discards it.
#
#   The spread between the two is still what says whether to trust the
#   result, and every caller prints it.
perf_baseline_ns() {
    awk -v a="$1" -v b="$2" 'BEGIN{ print (a<b) ? a : b }'
}

# perf_spread_pct A B
#   Their disagreement as a percentage of their mean, which is how every
#   bracketed case states its own error bar. Third copy of this awk, so
#   it lives here now.
perf_spread_pct() {
    awk -v a="$1" -v b="$2" 'BEGIN{
        m=(a+b)/2; d=a-b; if(d<0) d=-d;
        if(m>0) printf "%.0f", 100*d/m; else print "n/a" }'
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
    _prf_nfail=0
    while [ "$_prf_i" -lt "$_prf_iters" ]; do
        _prf_t0="$(perf_now_ns)"
        eval "$_prf_cmd" > /dev/null 2>&1 || _prf_nfail=$(( _prf_nfail + 1 ))
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
    _prf_med="$(perf_median_of_file "$_prf_samples")"
    _prf_p95="$(sort -n "$_prf_samples" | awk 'BEGIN{c=0} {a[c++]=$1} END{
        i=int(0.95*(c-1)+0.5); print a[i]}')"
    _prf_min="$(sort -n "$_prf_samples" | head -1)"
    rm -f "$_prf_samples"
    _prf_med_ms="$(awk -v n="$_prf_med" 'BEGIN{printf "%.3f", n/1e6}')"
    _prf_p95_ms="$(awk -v n="$_prf_p95" 'BEGIN{printf "%.3f", n/1e6}')"
    _prf_min_ms="$(awk -v n="$_prf_min" 'BEGIN{printf "%.3f", n/1e6}')"
    printf "Benchmark%-24s %4d  median=%8s ms  p95=%8s ms  min=%8s ms\n" \
        "$_prf_label" "$_prf_iters" "$_prf_med_ms" "$_prf_p95_ms" "$_prf_min_ms"
    if [ "$_prf_nfail" -gt 0 ]; then
        echo "  FAILED: $_prf_nfail of $_prf_iters iterations exited non-zero;" \
             "the times above are of an error path, not of $_prf_label"
        _prf_note_failure "$_prf_cmd"
        return 1
    fi
}

# _prf_note_failure "command..."
#   Marks the case failed and shows why, by running the command once more
#   with its output visible. run.sh fails the case on the mark, whatever
#   the case's own exit status. The mark is a file, not a variable,
#   because perf_median_ns runs in a $(...) subshell. Its path is
#   absolute ($PERF_FAIL_MARK, set by run.sh) so a case may cd.
#
#   Both timing helpers used to throw the exit status away. Every case
#   aimed at a service the target did not have still printed a time: it
#   was slinitctl's "could not be loaded" reply, ~1.1 ms, indistinguish-
#   able from a real answer. Ten cases did that for socklog and crond
#   once ceres was reinstalled without them, and `signal WINCH` had
#   timed a client-side parse error since the case was written.
#   A command expected to fail must say so with `|| true`, as 450, 690
#   and 780 do.
_prf_note_failure() {
    _prf_mark="${PERF_FAIL_MARK:-./.perf-failed}"
    [ -e "$_prf_mark" ] && return 0
    : > "$_prf_mark"
    echo "  FAILED: a timed command exited non-zero: $1" >&2
    echo "  its output on one more run:" >&2
    eval "$1" 2>&1 | head -5 | sed 's/^/    | /' >&2
}

# perf_require_syslog
#   Returns non-zero, with a SKIP line, unless a logger(1) message reaches
#   the journal. The journal-write cases pump logger and read the tag
#   back; on a host with nothing listening on /dev/log every logger call
#   fails and the read finds nothing, so they would time an empty fetch.
#   Use as:  perf_require_syslog || { return 0 2>/dev/null || exit 0; }
perf_require_syslog() {
    _prs_tag="perf_syslog_probe_$$"
    if logger -t "$_prs_tag" probe 2>/dev/null; then
        _prs_n=0
        while [ "$_prs_n" -lt 10 ]; do
            slinit-journalctl -t "$_prs_tag" -n 1 2>/dev/null | grep -q probe && return 0
            sleep 0.1
            _prs_n=$(( _prs_n + 1 ))
        done
    fi
    echo "SKIP: logger(1) messages do not reach the journal on this host" \
         "(nothing on /dev/log?)"
    return 1
}

# The suite's own target services, provisioned by run.sh (lib/fixture.sh)
# before the first case and removed after the last. Cases that signal,
# reload, setenv or catlog a service use these instead of whatever the
# host happens to run: they used socklog and crond, and a host without
# them turned ten cases into timings of an error reply.
PERF_TARGET=perf-target
PERF_TARGET_DEP=perf-target-dep
