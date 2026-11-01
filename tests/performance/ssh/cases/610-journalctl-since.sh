# 610-journalctl-since — `--since 1min` time-range filter cost.
# Journal read with a time bound instead of `-n N`. Uses relative
# time syntax the same way admins do in tail-loops. Similar-cost
# to `-n 100` if the filter is push-down; wider means the reader
# is pulling too much and post-filtering.
#
# `-1m`, the relative form slinit-journalctl(8) documents. The case used
# systemd's "1 min ago", which slinit-journalctl rejects, so it timed
# the parse error until the perf harness began checking exit status.
perf_run_iters "$ITERS" "JournalSince_1min" "slinit-journalctl --since -1m -n 100"
