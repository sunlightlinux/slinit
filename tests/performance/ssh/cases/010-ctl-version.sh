# 10-ctl-version — baseline: `slinitctl --version` prints a string.
# No control-socket round-trip, so this measures the CLI binary's
# fork/exec + package-init cost only. Used as the noise floor for
# every other slinitctl-* benchmark.
perf_run_iters "$ITERS" "CtlVersion" "slinitctl --version"
