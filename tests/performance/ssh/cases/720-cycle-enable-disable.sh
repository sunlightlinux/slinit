# 720-cycle-enable-disable — enable+disable cycle on a throwaway.
# enable = add waits-for to boot + start (starts svc + FS write of
# symlink). disable = remove waits-for from boot + stop. So this
# cycle is HEAVIER than 710's start+stop by exactly the symlink
# lifecycle overhead (create + unlink under /etc/slinit.d/boot.d/
# or wherever boot's waits-for.d points).
_name="perf-cycle-ed-$$"
_svcfile="/etc/slinit.d/$_name"
printf "type = process\ncommand = /bin/sleep 3600\nrestart = no\n" > "$_svcfile"
slinitctl load $_name > /dev/null 2>&1 || true

perf_run_iters "$ITERS" "Cycle_Enable_Disable_process" \
    "slinitctl enable $_name && slinitctl disable $_name"

slinitctl unload $_name > /dev/null 2>&1
rm -f "$_svcfile"
