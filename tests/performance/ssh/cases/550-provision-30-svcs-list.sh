# 550-provision-30-svcs-list — provision 30 throwaway svcs +
# start them all + `slinitctl list` (should show 30 more entries) +
# teardown. Measures the per-svc list cost as N grows; ceres
# baseline is 13 svcs — after provisioning we hit 43. `list`
# cost should scale roughly linearly with N.
_prefix="perf-throwaway-many-$$"
_before=$(slinitctl list 2>/dev/null | wc -l)
_i=0
while [ $_i -lt 30 ]; do
    printf "type = scripted\ncommand = /bin/true\n" > "/etc/slinit.d/$_prefix-$_i"
    slinitctl start "$_prefix-$_i" > /dev/null 2>&1
    _i=$((_i + 1))
done

_t0=$(perf_now_ns); slinitctl list > /dev/null; _t1=$(perf_now_ns)
_list_delta=$(( _t1 - _t0 ))

_after=$(slinitctl list 2>/dev/null | wc -l)

# Teardown
_i=0
while [ $_i -lt 30 ]; do
    slinitctl stop   "$_prefix-$_i" > /dev/null 2>&1
    slinitctl unload "$_prefix-$_i" > /dev/null 2>&1
    rm -f "/etc/slinit.d/$_prefix-$_i"
    _i=$((_i + 1))
done

printf "BenchmarkList_N%d_after_+30_provision  1  list_ms=%.3f  before=%d  after=%d\n" \
    "$_after" "$(awk -v n=$_list_delta 'BEGIN{print n/1e6}')" "$_before" "$_after"
