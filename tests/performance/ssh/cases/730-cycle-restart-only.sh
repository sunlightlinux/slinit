# 730-cycle-restart-only — restart cycle (one slinitctl, a stop and then a
# start, each waited for) on a type=process throwaway. Compare vs 710
# (start && stop as two slinitctl invocations): the difference is about one
# CLI fork/exec, the rest being the same server-side transitions. Before
# 3.11.0 restart did not wait for either, so its figure was only the cost of
# sending the two requests.
_name="perf-cycle-rst-$$"
_svcfile="/etc/slinit.d/$_name"
printf "type = process\ncommand = /bin/sleep 3600\nrestart = no\n" > "$_svcfile"
# Prime it — restart needs a started service to be meaningful.
slinitctl start $_name > /dev/null 2>&1

perf_run_iters "$ITERS" "Cycle_Restart_process" \
    "slinitctl restart $_name"

slinitctl stop   $_name > /dev/null 2>&1
slinitctl unload $_name > /dev/null 2>&1
rm -f "$_svcfile"
