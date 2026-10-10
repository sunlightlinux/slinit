# 790-list-scaling-100 — provision 100 throwaways (raising N
# from 13 to 113), measure `list` cost at that size. Extends
# `550`'s 30-svc measurement to a 10x-larger set to catch any
# quadratic/superlinear scaling that a linear-N test would miss.
#
# Both sides are medians of warmed-up runs. They used to be ONE timed
# `list` each, and a single sample of a ~1.2 ms command cannot measure a
# ratio: the case reported 1.06x on one run and 0.87x on the next, and
# 0.87x says adding 100 services made `list` faster. Whatever the real
# scaling is, a figure that can come out below 1.0 cannot detect it.
#
# $ITERS samples a side rather than a fixed 15, for about 70 ms of extra
# work. Doubling the samples was expected to tighten a noisy figure and
# did something more useful: 15 samples gave 1.15x and 30 gave 1.14x on
# the same install, so ~1.14x is signal, not spread — adding 100
# services really does cost `list` about 14%. The 1.00x seen once before
# that was on a different install with a different service set, not a
# contradiction of these two.
_prefix="perf-list100-$$"
_before=$(slinitctl list 2>/dev/null | wc -l)

_list_before_ns=$(perf_median_ns "$ITERS" "slinitctl list")

# Provision 100 svcs — start them so they populate the list
_i=0
while [ $_i -lt 100 ]; do
    printf "type = scripted\ncommand = /bin/true\n" > "/etc/slinit.d/$_prefix-$_i"
    slinitctl start "$_prefix-$_i" > /dev/null 2>&1
    _i=$((_i + 1))
done

_after=$(slinitctl list 2>/dev/null | wc -l)

# Measure list at the larger size
_list_after_ns=$(perf_median_ns "$ITERS" "slinitctl list")

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

printf "BenchmarkList_scaling %s  N=%d→%d  before=%s ms  after=%s ms  ratio=%sx\n" \
    "$ITERS" "$_before" "$_after" "$_before_ms" "$_after_ms" "$_ratio"
