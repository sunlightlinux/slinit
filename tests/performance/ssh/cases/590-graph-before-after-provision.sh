# 590-graph-before-after-provision — DOT graph export at N=13
# (default) vs N=43 (after +30 throwaways). Reveals graph
# render cost scaling — should be roughly linear in the number
# of nodes+edges. Also confirms the added svcs actually reach
# the graph output (correctness spot-check).
_before_edges=$(slinitctl graph 2>/dev/null | wc -l)
_t0=$(perf_now_ns); slinitctl graph > /dev/null; _t1=$(perf_now_ns)
_before_ms=$(awk -v n=$((_t1-_t0)) 'BEGIN{printf "%.3f", n/1e6}')

# Provision 30 throwaways
_prefix="perf-graph-many-$$"
_i=0
while [ $_i -lt 30 ]; do
    printf "type = scripted\ncommand = /bin/true\n" > "/etc/slinit.d/$_prefix-$_i"
    slinitctl start "$_prefix-$_i" > /dev/null 2>&1
    _i=$((_i + 1))
done

_after_edges=$(slinitctl graph 2>/dev/null | wc -l)
_t0=$(perf_now_ns); slinitctl graph > /dev/null; _t1=$(perf_now_ns)
_after_ms=$(awk -v n=$((_t1-_t0)) 'BEGIN{printf "%.3f", n/1e6}')

# Teardown
_i=0
while [ $_i -lt 30 ]; do
    slinitctl stop   "$_prefix-$_i" > /dev/null 2>&1
    slinitctl unload "$_prefix-$_i" > /dev/null 2>&1
    rm -f "/etc/slinit.d/$_prefix-$_i"
    _i=$((_i + 1))
done

printf "BenchmarkGraph_scaling  1  before_lines=%d  before_ms=%s  after_lines=%d  after_ms=%s\n" \
    "$_before_edges" "$_before_ms" "$_after_edges" "$_after_ms"
