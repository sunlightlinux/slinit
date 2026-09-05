# 400-slinitctl-is-started — minimal-payload probe query used
# by scripts checking "is svc up?" in loops. Should be at or
# below the baseline for status (less data to marshal). If it
# isn't materially faster than full `status`, the CLI's exit-
# code-only path isn't taking a shortcut and could.
perf_run_iters "$ITERS" "CtlIsStarted_boot" "slinitctl is-started boot"
