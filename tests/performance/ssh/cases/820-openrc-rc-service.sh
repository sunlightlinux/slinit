# 820-openrc-rc-service — measure OpenRC-shim latency. `rc-service
# status <svc>` is a thin wrapper over `slinitctl status`. Same
# call from a different CLI. Delta vs case 030 (direct slinitctl
# status = 1.081 ms) = shim overhead (extra fork/exec, arg
# translation).
perf_run_iters "$ITERS" "RcServiceStatus_socklog" "rc-service socklog status"
