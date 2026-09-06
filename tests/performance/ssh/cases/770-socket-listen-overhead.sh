# 770-socket-listen-overhead — start-cost delta of a service with
# `socket-listen` vs a plain service. slinit has to bind the
# socket + set up FD passing on start, before exec'ing the
# service command. Compare vs a plain scripted-start (case 740)
# to isolate the socket-setup overhead. Uses socket-activation
# = immediate so the svc starts eagerly on `slinitctl start`
# (matches 740's semantics — comparison stays clean).
_name="perf-sock-listen-$$"
_svcfile="/etc/slinit.d/$_name"
_sock="/tmp/perf-sock-$$"
cat > "$_svcfile" <<EOF
type = scripted
command = /bin/true
socket-listen = $_sock
socket-activation = immediate
EOF
slinitctl load "$_name" > /dev/null 2>&1 || true

perf_run_iters "$ITERS" "CtlStart_socket_listen_svc" "slinitctl start $_name"

# Teardown
rm -f "$_sock"
slinitctl stop "$_name"   > /dev/null 2>&1
slinitctl unload "$_name" > /dev/null 2>&1
rm -f "$_svcfile"
