# 880-fd-store-preserve — startup cost of a svc with fd-store
# preserve enabled. slinit tracks any fds the svc stashes via
# sd_notify FDSTORE=1. Setup overhead is a per-svc map init +
# hook wiring on Stop.
_name="perf-fdstore-$$"
_svcfile="/etc/slinit.d/$_name"
cat > "$_svcfile" <<EOF
type = process
command = /bin/true
file-descriptor-store-preserve = yes
EOF
slinitctl load "$_name" > /dev/null 2>&1 || true

perf_run_iters "$ITERS" "CtlStart_fdstore_svc" "slinitctl start $_name"

slinitctl unload "$_name" > /dev/null 2>&1
rm -f "$_svcfile"
