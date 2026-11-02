# 570-boot-time-under-writes — measure `slinitctl boot-time`
# latency while a background logger emitter fires 200 events
# per iteration. `boot-time` walks per-service timings; if the
# journal write path shares a mutex with the per-service state
# table, boot-time would stall. Healthy slinit: boot-time
# latency close to isolated baseline (~1.1 ms).
# Needs logger(1) to reach the journal; see perf_require_syslog.
perf_require_syslog || { return 0 2>/dev/null || exit 0; }
_writer() {
    _n=0
    while [ $_n -lt 200 ]; do
        logger -t "perf_bt_writer_$1" "flood-$_n"
        _n=$((_n + 1))
    done
}
_boot_time_under_load() {
    _writer "$1" &
    _wpid=$!
    slinitctl boot-time > /dev/null 2>&1
    wait "$_wpid" 2>/dev/null
}
_iter=0
perf_run_iters "$ITERS" "BootTime_UnderWrites_200" \
    '_iter=$((_iter+1)); _boot_time_under_load $_iter'
_tags=
_i=1
while [ "$_i" -le "$ITERS" ]; do _tags="$_tags perf_bt_writer_$_i"; _i=$((_i + 1)); done
# shellcheck disable=SC2086
perf_report_delivered $((ITERS * 200)) $_tags
