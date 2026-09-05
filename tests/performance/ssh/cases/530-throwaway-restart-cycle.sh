# 530-throwaway-restart-cycle — provision one throwaway svc,
# then hammer `slinitctl restart` on it 5 times per iteration.
# Each restart is a stop+start pair; measures the tight-loop
# state-machine transition cost + notification propagation.
_name="perf-throwaway-rst-$$"
_svcfile="/etc/slinit.d/$_name"
printf "type = scripted\ncommand = /bin/true\n" > "$_svcfile"
slinitctl start "$_name" > /dev/null 2>&1

perf_run_iters "$ITERS" "ServiceRestartCycle_x5" \
    "slinitctl restart $_name > /dev/null 2>&1; slinitctl restart $_name > /dev/null 2>&1; \
     slinitctl restart $_name > /dev/null 2>&1; slinitctl restart $_name > /dev/null 2>&1; \
     slinitctl restart $_name > /dev/null 2>&1"

# Teardown
slinitctl stop "$_name"   > /dev/null 2>&1
slinitctl unload "$_name" > /dev/null 2>&1
rm -f "$_svcfile"
