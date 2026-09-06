# 850-slinit-monitor-help — slinit-monitor is a TUI (never exits
# without user input), so we measure only its cold-start `--help`
# path: fork/exec + arg-parse + print + exit. Approximates the
# TUI init cost without needing a real tty. Same shape as
# CtlVersion but for the monitor binary.
perf_run_iters "$ITERS" "SlinitMonitorHelp" "slinit-monitor --help"
