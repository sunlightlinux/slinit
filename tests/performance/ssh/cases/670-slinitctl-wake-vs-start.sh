# 670-slinitctl-wake-vs-start — start (marks active) vs wake
# (re-attaches to active dependents without marking) on a
# throwaway `type=scripted`. Same code path except the "mark
# active" bookkeeping. If wake is materially cheaper, scripts
# that re-attach transient deps should prefer it.
_name="perf-throwaway-wake-$$"
_svcfile="/etc/slinit.d/$_name"
printf "type = scripted\ncommand = /bin/true\n" > "$_svcfile"

# Cycle: start (mark active) then release (remove mark, stop) —
# so the next start starts from STOPPED-inactive state again.
perf_run_iters "$ITERS" "CtlStart_cycle_throwaway" \
    "slinitctl start $_name && slinitctl release $_name"

# For wake, we need a service that's stopped-but-loaded. `release`
# after start puts it there. So the pair start/release drives
# the cycle regardless.

slinitctl stop "$_name"   > /dev/null 2>&1
slinitctl unload "$_name" > /dev/null 2>&1
rm -f "$_svcfile"
