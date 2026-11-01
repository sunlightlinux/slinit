# 820-openrc-rc-service — measure OpenRC-shim latency. `rc-service
# status <svc>` is a thin wrapper over `slinitctl status`. Same
# call from a different CLI. Delta vs case 030 (direct slinitctl
# status = 1.081 ms) = shim overhead (extra fork/exec, arg
# translation).
# The OpenRC shims are a separate package (slinit-openrc-shims).
if ! command -v rc-service > /dev/null; then
    echo "SKIP: rc-service is not installed (package slinit-openrc-shims)"
    return 0 2>/dev/null || exit 0
fi
perf_run_iters "$ITERS" "RcServiceStatus_perftarget" "rc-service $PERF_TARGET status"
