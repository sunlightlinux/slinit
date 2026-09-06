# 790-list-scaling-100 — provision 100 throwaways (raising N
# from 13 to 113), measure `list` cost at that size. Extends
# `550`'s 30-svc measurement to a 10x-larger set to catch any
# quadratic/superlinear scaling that a linear-N test would miss.
_prefix="perf-list100-$$"
_before=$(slinitctl list 2>/dev/null | wc -l)

_t0=$(perf_now_ns); slinitctl list > /dev/null; _t1=$(perf_now_ns)
_list_before_ns=$(( _t1 - _t0 ))

# Provision 100 svcs — start them so they populate the list
_i=0
while [ $_i -lt 100 ]; do
    printf "type = scripted\ncommand = /bin/true\n" > "/etc/slinit.d/$_prefix-$_i"
    slinitctl start "$_prefix-$_i" > /dev/null 2>&1
    _i=$((_i + 1))
done

_after=$(slinitctl list 2>/dev/null | wc -l)

# Measure list at the larger size
_t0=$(perf_now_ns); slinitctl list > /dev/null; _t1=$(perf_now_ns)
_list_after_ns=$(( _t1 - _t0 ))

# Teardown
_i=0
while [ $_i -lt 100 ]; do
    slinitctl stop   "$_prefix-$_i" > /dev/null 2>&1
    slinitctl unload "$_prefix-$_i" > /dev/null 2>&1
    rm -f "/etc/slinit.d/$_prefix-$_i"
    _i=$((_i + 1))
done

_before_ms=$(awk -v n=$_list_before_ns 'BEGIN{printf "%.3f", n/1e6}')
_after_ms=$(awk -v n=$_list_after_ns 'BEGIN{printf "%.3f", n/1e6}')
_ratio=$(awk -v a=$_list_after_ns -v b=$_list_before_ns 'BEGIN{if(b>0) printf "%.2f", a/b; else print "n/a"}')

printf "BenchmarkList_scaling  1  N=%d→%d  before=%s ms  after=%s ms  ratio=%sx\n" \
    "$_before" "$_after" "$_before_ms" "$_after_ms" "$_ratio"
