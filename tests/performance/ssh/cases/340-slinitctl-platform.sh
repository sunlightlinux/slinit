# 340-slinitctl-platform — platform-detect roundtrip. Detects
# virtualization / container runtime by probing /proc + /sys.
# One-off info query but hit often by scripts branching on env.
perf_run_iters "$ITERS" "CtlPlatform" "slinitctl platform"
