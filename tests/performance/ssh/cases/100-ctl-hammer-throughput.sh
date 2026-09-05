# 100-ctl-hammer-throughput — throughput mode: how many
# `slinitctl status boot` invocations per second in a tight
# sequential loop of 200. Reports median ms for the WHOLE batch of
# 200 (so per-op cost = median / 200). Useful to compare against
# CtlStatus_boot's single-op median — if per-op cost here is higher
# than the single-op median, there's a warmup / cache-miss story
# worth investigating; if lower, the CLI fork/exec baseline is
# amortising badly against something.
perf_run_iters "$ITERS" "CtlHammer_200x_boot" \
    '_n=0; while [ $_n -lt 200 ]; do slinitctl status boot > /dev/null; _n=$((_n+1)); done'
