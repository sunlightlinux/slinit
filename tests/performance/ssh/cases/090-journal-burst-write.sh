# 090-journal-burst-write — burst-emit N log entries via `logger`
# into a case-scoped tag, then measure the time to fetch them all
# back via `slinit-journalctl -t <tag> -n N`. Reveals the write-
# path throughput (logger → socklog → journald) plus the tag-
# filtered read cost. Uses a fresh tag per iteration so entries
# don't accumulate. Batch size kept modest (200) because each
# logger invocation costs 5-10ms of fork/exec — with ITERS=10
# that is already 2000 fork/exec pairs (~20s).
_BURST_N="${PERF_BURST_N:-200}"
_burst_and_fetch() {
    _tag="perftest_$$_$1"
    _i=0
    while [ $_i -lt "$_BURST_N" ]; do
        logger -t "$_tag" "burst-msg-$_i"
        _i=$((_i + 1))
    done
    # Small settle so socklog forwarders drain into the journal
    # before we ask journalctl to see them.
    sleep 0.1
    slinit-journalctl -t "$_tag" -n "$_BURST_N" > /dev/null 2>&1
}
# Iteration index passed as $1 so the tag stays unique across
# perf_run_iters' `eval`-based invocation.
_iter=0
perf_run_iters "$ITERS" "JournalBurstWriteFetch_${_BURST_N}" \
    '_iter=$((_iter+1)); _burst_and_fetch $_iter'
