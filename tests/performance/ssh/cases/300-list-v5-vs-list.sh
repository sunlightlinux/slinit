# 300-list-v5-vs-list — protocol variant cost comparison.
# `list` uses control-protocol v7 (compact); `list5` uses v5
# (detailed, more fields). If v5 is much slower it points at
# per-record marshalling overhead worth carrying forward as a
# `list5`-avoid guideline for scripts polling frequently.
perf_run_iters "$ITERS" "CtlList_v7" "slinitctl list"
perf_run_iters "$ITERS" "CtlList5_v5" "slinitctl list5"
