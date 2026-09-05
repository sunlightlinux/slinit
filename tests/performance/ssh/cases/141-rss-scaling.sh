# 141-rss-scaling — measure RSS growth at 3 load points (500,
# 2500, 10000 ops) to distinguish a genuine per-op leak (delta
# scales linearly) from Go runtime arena growth that plateaus
# (delta stops rising after the first few hundred ops). Also
# lets us see whether repeated runs of the same size see the
# same growth (repeatable) or converge to zero (one-time init).
_read_rss() {
    awk '/^VmRSS:/ {print $2; exit}' /proc/1/status
}

_rss0=$(_read_rss)
echo "  t=0    RSS=${_rss0}kB (start)"

_i=0
while [ $_i -lt 500 ]; do
    slinitctl status boot > /dev/null
    _i=$((_i + 1))
done
_rss1=$(_read_rss)
echo "  after 500 ops   RSS=${_rss1}kB  delta=$(( _rss1 - _rss0 ))kB"

_i=0
while [ $_i -lt 2000 ]; do
    slinitctl status boot > /dev/null
    _i=$((_i + 1))
done
_rss2=$(_read_rss)
echo "  after 2500 ops  RSS=${_rss2}kB  delta=$(( _rss2 - _rss0 ))kB  (+$(( _rss2 - _rss1 ))kB since 500)"

_i=0
while [ $_i -lt 7500 ]; do
    slinitctl status boot > /dev/null
    _i=$((_i + 1))
done
_rss3=$(_read_rss)
echo "  after 10000 ops RSS=${_rss3}kB  delta=$(( _rss3 - _rss0 ))kB  (+$(( _rss3 - _rss2 ))kB since 2500)"

# Second run: same 2500 additional ops. If leak, RSS grows more;
# if arena growth, it plateaus.
_i=0
while [ $_i -lt 2500 ]; do
    slinitctl status boot > /dev/null
    _i=$((_i + 1))
done
sleep 1
_rss4=$(_read_rss)
echo "  after 12500 ops RSS=${_rss4}kB  delta=$(( _rss4 - _rss0 ))kB  (+$(( _rss4 - _rss3 ))kB since 10000)"

printf "BenchmarkPID1_RSS_Growth_Curve   1  0→500=%dkB  500→2500=%dkB  2500→10000=%dkB  10000→12500=%dkB\n" \
    $(( _rss1 - _rss0 )) $(( _rss2 - _rss1 )) $(( _rss3 - _rss2 )) $(( _rss4 - _rss3 ))
