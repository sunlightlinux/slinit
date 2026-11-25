# 820-openrc-rc-service — measure OpenRC-shim latency of
# `rc-service <svc> status`.
#
# Through 4.0.2 it exec'd `slinitctl status` and nothing else, so the
# delta vs case 030 (direct slinitctl status = 1.081 ms) was the shim's
# overhead. It now exits with OpenRC's status codes (0 started, 3
# stopped, ...), which it learns from a second call, `slinitctl
# is-failed`: two control round trips and two fork/execs per run. Read
# it against 030 + 450 (is-failed), not against 030 alone; figures
# recorded before the change are not comparable. perf-target is
# STARTED, so the exit status is 0 and the iterations are not counted
# as failures.
# The OpenRC shims are a separate package (slinit-openrc-shims).
if ! command -v rc-service > /dev/null; then
    echo "SKIP: rc-service is not installed (package slinit-openrc-shims)"
    return 0 2>/dev/null || exit 0
fi
perf_run_iters "$ITERS" "RcServiceStatus_perftarget" "rc-service $PERF_TARGET status"
