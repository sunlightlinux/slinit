# 570-boot-time-under-writes — measure `slinitctl boot-time`
# latency while a background logger emitter fires 200 events
# per iteration. `boot-time` walks per-service timings; if the
# journal write path shares a mutex with the per-service state
# table, boot-time would stall. Healthy slinit: boot-time
# latency close to isolated baseline (~1.1 ms).
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
