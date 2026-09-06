# 750-deep-chain-100 — build a 100-deep dep chain and measure
# `status` on the tip. Extends `560` (10-deep) to catch any
# O(depth) blow-up that a linear-scan implementation would hide
# at 10 nodes. Server-side dep walk is supposed to be O(1) via a
# cached state — validated at 100 nodes here.
_prefix="perf-chain100-$$"
_DEPTH=100

_i=0
while [ $_i -lt $_DEPTH ]; do
    if [ $_i -eq 0 ]; then
        printf "type = scripted\ncommand = /bin/true\n" > "/etc/slinit.d/$_prefix-$_i"
    else
        _prev=$(( _i - 1 ))
        printf "type = scripted\ncommand = /bin/true\ndepends-on: $_prefix-$_prev\n" > "/etc/slinit.d/$_prefix-$_i"
    fi
    _i=$((_i + 1))
done

# Start tip — transitively starts the chain
_tip=$(( _DEPTH - 1 ))
slinitctl start "$_prefix-$_tip" > /dev/null 2>&1

perf_run_iters "$ITERS" "Status_DeepChain100_tip" "slinitctl status $_prefix-$_tip"

# Teardown in reverse
_i=$(( _DEPTH - 1 ))
while [ $_i -ge 0 ]; do
    slinitctl stop   "$_prefix-$_i" > /dev/null 2>&1
    slinitctl unload "$_prefix-$_i" > /dev/null 2>&1
    rm -f "/etc/slinit.d/$_prefix-$_i"
    _i=$((_i - 1))
done
