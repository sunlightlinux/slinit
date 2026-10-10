# 560-deep-dep-chain — build a 10-deep dep chain (svc_9 depends
# on svc_8, ..., svc_1 depends on svc_0), start the tip, measure
# `status` on the tip. Teardown reverses the chain.
#
# `status` does NOT walk the chain, which this comment used to claim:
# it renders that one service's own fields and its journal tail, and
# traverses nothing — checked against the running daemon. So this is a
# baseline figure for status with a chain present, not a measurement of
# depth, and it cannot reveal depth scaling on its own. `750` does that,
# by building two depths in one run and reporting the ratio.
_prefix="perf-chain-$$"
# Build chain: svc-0 has no dep, svc-1 depends on svc-0, ...
_i=0
while [ $_i -lt 10 ]; do
    if [ $_i -eq 0 ]; then
        printf "type = scripted\ncommand = /bin/true\n" > "/etc/slinit.d/$_prefix-$_i"
    else
        _prev=$(( _i - 1 ))
        printf "type = scripted\ncommand = /bin/true\ndepends-on: $_prefix-$_prev\n" > "/etc/slinit.d/$_prefix-$_i"
    fi
    _i=$((_i + 1))
done

# Start tip (transitively starts the chain)
slinitctl start "$_prefix-9" > /dev/null 2>&1

# Measure status on tip (walks chain)
perf_run_iters "$ITERS" "Status_DeepChain10_tip" "slinitctl status $_prefix-9"

# Teardown: reverse order (tip first)
_i=9
while [ $_i -ge 0 ]; do
    slinitctl stop   "$_prefix-$_i" > /dev/null 2>&1
    slinitctl unload "$_prefix-$_i" > /dev/null 2>&1
    rm -f "/etc/slinit.d/$_prefix-$_i"
    _i=$((_i - 1))
done
