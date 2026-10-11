# 590-graph-before-after-provision — DOT graph export at N=13 (default)
# vs N=43 (after +30 throwaways). Reveals graph render cost scaling —
# should be roughly linear in the number of nodes+edges. Also confirms
# the added svcs actually reach the graph output (correctness check).
#
# Both arms are bracketed and reduced to their minimum, as in `790`.
# They were one timed `graph` each, and a single sample of a ~1.5 ms
# command cannot carry a ratio: the pair read 1.544/1.967 ms on one run
# and 1.515/2.400 on the next, which is a 20% swing in the ratio from
# nothing. Latency has a hard floor, so a contaminated sample is always
# the slow one and the minimum of two discards it; the pair's own
# disagreement is printed as the error bar.
_before_edges=$(slinitctl graph 2>/dev/null | wc -l)
_gb1=$(perf_median_ns "$ITERS" "slinitctl graph")

# Provision 30 throwaways
_prefix="perf-graph-many-$$"
_i=0
while [ $_i -lt 30 ]; do
    printf "type = scripted\ncommand = /bin/true\n" > "/etc/slinit.d/$_prefix-$_i"
    slinitctl start "$_prefix-$_i" > /dev/null 2>&1
    _i=$((_i + 1))
done

_after_edges=$(slinitctl graph 2>/dev/null | wc -l)
_ga1=$(perf_median_ns "$ITERS" "slinitctl graph")
_ga2=$(perf_median_ns "$ITERS" "slinitctl graph")

# Teardown
_i=0
while [ $_i -lt 30 ]; do
    slinitctl stop   "$_prefix-$_i" > /dev/null 2>&1
    slinitctl unload "$_prefix-$_i" > /dev/null 2>&1
    rm -f "/etc/slinit.d/$_prefix-$_i"
    _i=$((_i + 1))
done

# Second small-N bracket, now the extra services are gone again.
_gb2=$(perf_median_ns "$ITERS" "slinitctl graph")

_before_ns=$(perf_baseline_ns "$_gb1" "$_gb2")
_after_ns=$(perf_baseline_ns "$_ga1" "$_ga2")
_before_ms=$(awk -v n="$_before_ns" 'BEGIN{printf "%.3f", n/1e6}')
_after_ms=$(awk -v n="$_after_ns" 'BEGIN{printf "%.3f", n/1e6}')
_ratio=$(awk -v a="$_after_ns" -v b="$_before_ns" 'BEGIN{if(b>0) printf "%.2f", a/b; else print "n/a"}')

printf "BenchmarkGraph_scaling %s  N=%d→%d lines  before=%s ms (spread %s%%)  after=%s ms (spread %s%%)  ratio=%sx\n" \
    "$ITERS" "$_before_edges" "$_after_edges" \
    "$_before_ms" "$(perf_spread_pct "$_gb1" "$_gb2")" \
    "$_after_ms"  "$(perf_spread_pct "$_ga1" "$_ga2")" \
    "$_ratio"
