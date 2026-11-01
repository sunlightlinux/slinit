# 190-slinit-check-offline — offline config linter latency. Doesn't
# touch the running slinit at all — pure file-read + parse of one
# service. Isolates the pkg/config parser cost from the control-socket
# path. The target is the fixture's perf-target-dep: one small internal
# service with no dependencies, so this is one file; 890 does the
# recursive check of `boot`.
#
# slinit-check takes a service NAME, as dinit-check does. This case
# passed /etc/slinit.d/boot, a path, which is looked up as a name and
# never found: it timed "service description not found" from the day it
# was written.
if ! command -v slinit-check > /dev/null 2>&1; then
    echo "SKIP: slinit-check not installed on target"
    exit 0
fi
perf_run_iters "$ITERS" "SlinitCheckOffline_one_file" "slinit-check $PERF_TARGET_DEP"
