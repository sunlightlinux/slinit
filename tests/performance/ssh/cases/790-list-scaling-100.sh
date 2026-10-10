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
# The small size is measured TWICE, once before provisioning and once
# after tearing down, and the ratio uses their mean. That is not
# statistical dressing — it is the only way this case produces a figure
# that is not dominated by when it ran.
#
# Its history, each attempt fixing what the last one revealed. One timed
# `list` per side gave 1.06x, then 0.87x. Medians of 15 warmed runs gave
# 1.00x, 1.15x. Thirty samples gave 1.14x, which agreed closely enough
# with 1.15x to look like a real 14% cost, and then 1.08x and 0.71x
# arrived. 0.71x says adding 100 services made `list` 29% faster, so the
# problem was never sampling error.
#
# What it was: the "before" reading is taken at whatever moment the case
# starts, and when `750` runs immediately ahead of it the daemon has
# just unloaded 35 services. That run measured before=1.839 ms against
# an after=1.298 ms — the "before" was the outlier. Three warm-up reads
# do not settle a daemon that has just done a burst of unload work, and
# more samples cannot help, because the drift is between the two
# measurements rather than inside either one.
#
# Bracketing cancels drift that happens across the case: a before-sample
# on each side of the provisioning, and both are printed so a reader can
# see how far apart they were. If they disagree sharply, the ratio is
# worth nothing and the two figures say so directly.
_prefix="perf-list100-$$"
_before=$(slinitctl list 2>/dev/null | wc -l)

_list_before1_ns=$(perf_median_ns "$ITERS" "slinitctl list")

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

# Teardown, then the second small-size reading.
_i=0
while [ $_i -lt 100 ]; do
    slinitctl stop   "$_prefix-$_i" > /dev/null 2>&1
    slinitctl unload "$_prefix-$_i" > /dev/null 2>&1
    rm -f "/etc/slinit.d/$_prefix-$_i"
    _i=$((_i + 1))
done

# Second small-size reading, now that the extra services are gone.
_list_before2_ns=$(perf_median_ns "$ITERS" "slinitctl list")

_b1_ms=$(awk -v n=$_list_before1_ns 'BEGIN{printf "%.3f", n/1e6}')
_b2_ms=$(awk -v n=$_list_before2_ns 'BEGIN{printf "%.3f", n/1e6}')
_after_ms=$(awk -v n=$_list_after_ns 'BEGIN{printf "%.3f", n/1e6}')

# The two brackets' mean is the baseline; their disagreement is printed
# as the measurement's own error bar, because a ratio taken against a
# drifting baseline means nothing and should look like it.
_base_ns=$(awk -v a=$_list_before1_ns -v b=$_list_before2_ns 'BEGIN{print (a+b)/2}')
_base_ms=$(awk -v n="$_base_ns" 'BEGIN{printf "%.3f", n/1e6}')
_ratio=$(awk -v a=$_list_after_ns -v b="$_base_ns" 'BEGIN{if(b>0) printf "%.2f", a/b; else print "n/a"}')
_spread=$(awk -v a=$_list_before1_ns -v b=$_list_before2_ns 'BEGIN{
    m=(a+b)/2; d=a-b; if(d<0) d=-d; if(m>0) printf "%.0f", 100*d/m; else print "n/a"}')

printf "BenchmarkList_scaling %s  N=%d→%d  base=%s ms (%s/%s, spread %s%%)  after=%s ms  ratio=%sx\n" \
    "$ITERS" "$_before" "$_after" "$_base_ms" "$_b1_ms" "$_b2_ms" "$_spread" "$_after_ms" "$_ratio"
