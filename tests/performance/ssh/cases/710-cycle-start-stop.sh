# 710-cycle-start-stop — start+stop cycle on a type=process
# throwaway with a long-running cmd. Each iteration issues one
# start (fires the process) + one stop (SIGTERM + reap). Halved
# gives per-op approximation. Uses `sleep 3600` so stop actually
# has something running to stop (not a no-op on already-stopped
# svc).
_name="perf-cycle-ss-$$"
_svcfile="/etc/slinit.d/$_name"
printf "type = process\ncommand = /bin/sleep 3600\nrestart = no\n" > "$_svcfile"
slinitctl load $_name > /dev/null 2>&1 || true

perf_run_iters "$ITERS" "Cycle_Start_Stop_process" \
    "slinitctl start $_name && slinitctl stop $_name"

slinitctl unload $_name > /dev/null 2>&1
rm -f "$_svcfile"
