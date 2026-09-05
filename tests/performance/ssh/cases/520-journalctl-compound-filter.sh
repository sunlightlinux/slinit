# 520-journalctl-compound-filter — combined `-t <tag> -p warn`
# vs each filter alone vs unfiltered. Baseline set:
#   - unfiltered  (from 220-priority baseline shape)
#   - `-t tag`    (single filter, from 210)
#   - `-p warn`   (single filter, from 220)
#   - `-t tag -p warn`  (BOTH — the new axis)
# Compound filter should be at most as slow as the more-restrictive
# single filter (both push-downs stacking). Higher than either
# individually would point at inefficient AND-composition in the
# scan predicate.
_tag="perfcompound_$$"
_n=0
while [ $_n -lt 100 ]; do
    logger -t "$_tag" -p user.warn "compound-$_n"
    _n=$((_n + 1))
done
sleep 0.2
perf_run_iters "$ITERS" "JournalFetch_nofilter" "slinit-journalctl -n 100"
perf_run_iters "$ITERS" "JournalFetch_tag_only" "slinit-journalctl -t $_tag -n 100"
perf_run_iters "$ITERS" "JournalFetch_warn_only" "slinit-journalctl -p warning -n 100"
perf_run_iters "$ITERS" "JournalFetch_tag_and_warn" "slinit-journalctl -t $_tag -p warning -n 100"
