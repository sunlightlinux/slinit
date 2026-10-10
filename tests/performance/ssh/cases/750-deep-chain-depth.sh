# 750-deep-chain-depth — does anything about a dependency chain cost
# O(depth)? Builds a shallow and a deep chain in the same run and
# compares, so the answer is a ratio measured under one set of
# conditions rather than two numbers from two cases at two times.
#
# This case used to be called -100 and build a 100-deep chain. That
# cannot load: MaxDepDepth is 32, so `slinitctl start` on the tip
# answered "service could not be loaded" and the case, which discarded
# that output, went on to time `slinitctl status` against the same error
# reply. An error is cheaper than a real status, which is why the
# 100-deep figure came out FASTER than 560's 10-deep one on every run —
# 1.824 vs 2.192 ms, then 1.378 vs 2.296 ms. For its whole life this
# case measured an error message.
#
# Two things stop that happening again: the depths are 4 and 31, both
# inside the limit, and the tip's start is checked rather than
# discarded, so a chain that will not load fails the case loudly.
#
# The second claim is also new. Measuring `status` on a tip cannot show
# an O(depth) dependency walk, because status renders that one service's
# own fields and its journal tail and traverses nothing — checked
# against the running daemon. So status is measured as the claim it can
# support, that its cost does NOT grow with depth, and the walk is
# measured where it happens: the cold start of the chain.

# build_chain PREFIX DEPTH — writes DEPTH service files, 0 depending on
# nothing and each later one on its predecessor.
build_chain() {
    _bc_prefix="$1"; _bc_depth="$2"
    _bc_i=0
    while [ "$_bc_i" -lt "$_bc_depth" ]; do
        if [ "$_bc_i" -eq 0 ]; then
            printf "type = scripted\ncommand = /bin/true\n" \
                > "/etc/slinit.d/$_bc_prefix-$_bc_i"
        else
            _bc_prev=$(( _bc_i - 1 ))
            printf "type = scripted\ncommand = /bin/true\ndepends-on: %s-%s\n" \
                "$_bc_prefix" "$_bc_prev" > "/etc/slinit.d/$_bc_prefix-$_bc_i"
        fi
        _bc_i=$(( _bc_i + 1 ))
    done
}

# stop_chain PREFIX DEPTH — tip first, so no stop is refused for having
# a started dependent.
stop_chain() {
    _sc_prefix="$1"; _sc_depth="$2"
    _sc_i=$(( _sc_depth - 1 ))
    while [ "$_sc_i" -ge 0 ]; do
        slinitctl stop "$_sc_prefix-$_sc_i" > /dev/null 2>&1
        _sc_i=$(( _sc_i - 1 ))
    done
}

remove_chain() {
    _rc_prefix="$1"; _rc_depth="$2"
    _rc_i=$(( _rc_depth - 1 ))
    while [ "$_rc_i" -ge 0 ]; do
        slinitctl stop   "$_rc_prefix-$_rc_i" > /dev/null 2>&1
        slinitctl unload "$_rc_prefix-$_rc_i" > /dev/null 2>&1
        rm -f "/etc/slinit.d/$_rc_prefix-$_rc_i"
        _rc_i=$(( _rc_i - 1 ))
    done
}

# cold_start_ns PREFIX DEPTH ITERS — median ns for starting the tip with
# the whole chain stopped, which is the transitive dependency walk.
cold_start_ns() {
    _cs_prefix="$1"; _cs_depth="$2"; _cs_iters="$3"
    _cs_tip=$(( _cs_depth - 1 ))
    _cs_f="$(mktemp)"
    _cs_i=0
    while [ "$_cs_i" -lt "$_cs_iters" ]; do
        stop_chain "$_cs_prefix" "$_cs_depth"
        _cs_t0="$(perf_now_ns)"
        slinitctl start "$_cs_prefix-$_cs_tip" > /dev/null 2>&1
        _cs_t1="$(perf_now_ns)"
        echo $(( _cs_t1 - _cs_t0 )) >> "$_cs_f"
        _cs_i=$(( _cs_i + 1 ))
    done
    perf_median_of_file "$_cs_f"
    rm -f "$_cs_f"
}

_SHALLOW=4
_DEEP=31      # MaxDepDepth is 32; 31 is the deepest chain that loads

_ps="perf-chain-s-$$"
_pd="perf-chain-d-$$"

build_chain "$_ps" "$_SHALLOW"
build_chain "$_pd" "$_DEEP"

_s_tip="$_ps-$(( _SHALLOW - 1 ))"
_d_tip="$_pd-$(( _DEEP - 1 ))"

# Checked, not discarded: this is what let the case measure an error.
for _tip in "$_s_tip" "$_d_tip"; do
    if ! slinitctl start "$_tip" > /dev/null 2>&1; then
        echo "FAIL: could not start $_tip — the chain does not load, so there is" \
             "nothing here to measure (MaxDepDepth is 32)" >&2
        remove_chain "$_pd" "$_DEEP"
        remove_chain "$_ps" "$_SHALLOW"
        exit 1
    fi
done

# Claim 1: status does not grow with depth.
#
# The ratio's own resolution, measured across three runs: 1.05x, 0.99x,
# 1.20x. So anything inside roughly 0.9x-1.2x is "no growth detected"
# and a 1.2x reading is not a finding. What would be one is a ratio
# approaching the depth ratio itself, 7.8x, which is what a per-node
# walk on the status path would produce.
_st_s=$(perf_median_ns "$ITERS" "slinitctl status $_s_tip")
_st_d=$(perf_median_ns "$ITERS" "slinitctl status $_d_tip")
_st_ratio=$(awk -v a="$_st_d" -v b="$_st_s" 'BEGIN{if(b>0) printf "%.2f", a/b; else print "n/a"}')
printf "BenchmarkStatus_DeepChain_depth_ratio %4d  d%s=%s ms  d%s=%s ms  ratio=%sx (flat within 0.9-1.2x)\n" \
    "$ITERS" "$_SHALLOW" \
    "$(awk -v n="$_st_s" 'BEGIN{printf "%.3f", n/1e6}')" "$_DEEP" \
    "$(awk -v n="$_st_d" 'BEGIN{printf "%.3f", n/1e6}')" \
    "$_st_ratio"

# Claim 2: the cold start IS the dependency walk, so this is the ratio a
# linear scan would blow up. 10x depth with a linear walk means ~10x;
# anything far below that is the cached state doing its job.
_cs_iters=3
_cold_s=$(cold_start_ns "$_ps" "$_SHALLOW" "$_cs_iters")
_cold_d=$(cold_start_ns "$_pd" "$_DEEP" "$_cs_iters")
_cs_ratio=$(awk -v a="$_cold_d" -v b="$_cold_s" 'BEGIN{if(b>0) printf "%.2f", a/b; else print "n/a"}')
printf "BenchmarkColdStart_DeepChain_depth_ratio %4d  d%s=%s ms  d%s=%s ms  ratio=%sx (%sx depth)\n" \
    "$_cs_iters" "$_SHALLOW" \
    "$(awk -v n="$_cold_s" 'BEGIN{printf "%.3f", n/1e6}')" "$_DEEP" \
    "$(awk -v n="$_cold_d" 'BEGIN{printf "%.3f", n/1e6}')" \
    "$_cs_ratio" "$(awk -v a="$_DEEP" -v b="$_SHALLOW" 'BEGIN{printf "%.1f", a/b}')"

remove_chain "$_pd" "$_DEEP"
remove_chain "$_ps" "$_SHALLOW"
