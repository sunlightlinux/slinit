# 890-slinit-check-boot — offline linter latency on the default
# `boot` target. Recursively parses boot + every dep. Real
# admin-facing cost: how long does `slinit-check` take before a
# deploy. Compares to case 190 (slinit-check on a single trivial
# svc) — delta reveals the recursive dep-parse cost.
perf_run_iters "$ITERS" "SlinitCheck_boot_recursive" "slinit-check boot"
