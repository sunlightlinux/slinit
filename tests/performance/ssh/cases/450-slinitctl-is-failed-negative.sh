# 450-slinitctl-is-failed-negative — negative-case exit-code
# probe. is-failed returns 1 on a running svc; the CLI needs to
# format that as an exit code. Compare vs `400-is-started`
# (positive case) to see if the failure-path costs any extra.
perf_run_iters "$ITERS" "CtlIsFailed_boot_negative" "slinitctl is-failed boot || true"
