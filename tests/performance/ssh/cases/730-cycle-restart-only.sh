# 730-cycle-restart-only — pure restart cycle (stop + start folded
# into one control-socket op) on a type=process throwaway. Compare
# vs 710 (start && stop as two separate slinitctl invocations): if
# restart is cheaper than 710, the CLI fork/exec baseline (~1 ms
# per slinitctl process) dominates and merging into one op is a
# real optimization; if it's the same, the server-side state
# transitions dominate.
_name="perf-cycle-rst-$$"
_svcfile="/etc/slinit.d/$_name"
printf "type = process\ncommand = /bin/sleep 3600\nrestart = no\n" > "$_svcfile"
slinitctl load $_name > /dev/null 2>&1 || true
# Prime it — restart needs a started service to be meaningful.
slinitctl start $_name > /dev/null 2>&1

perf_run_iters "$ITERS" "Cycle_Restart_process" \
    "slinitctl restart $_name"

slinitctl stop   $_name > /dev/null 2>&1
slinitctl unload $_name > /dev/null 2>&1
rm -f "$_svcfile"
