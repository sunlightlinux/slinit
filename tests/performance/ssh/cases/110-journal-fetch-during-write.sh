# 110-journal-fetch-during-write — measure `slinit-journalctl -n 500`
# latency under sustained WRITE pressure. Backgrounds a `logger`
# emitter that fires 500 events / iteration, then times the fetch
# racing with that emitter. Reveals whether the reader path
# contends with the writer path (would show as elevated fetch
# latency vs baseline `50-journalctl-fetch-n100`).
# Needs logger(1) to reach the journal; see perf_require_syslog.
perf_require_syslog || { return 0 2>/dev/null || exit 0; }
_write_pressure() {
    _n=0
    while [ $_n -lt 500 ]; do
        logger -t "perf_writer_$1" "pressure-$_n"
        _n=$((_n + 1))
    done
}
_fetch_under_pressure() {
    _write_pressure "$1" &
    _wpid=$!
    slinit-journalctl -n 500 > /dev/null 2>&1
    wait "$_wpid" 2>/dev/null
}
_iter=0
perf_run_iters "$ITERS" "JournalFetchUnderWrite_500" \
    '_iter=$((_iter+1)); _fetch_under_pressure $_iter'
_tags=
_i=1
while [ "$_i" -le "$ITERS" ]; do _tags="$_tags perf_writer_$_i"; _i=$((_i + 1)); done
# shellcheck disable=SC2086
perf_report_delivered $((ITERS * 500)) $_tags
