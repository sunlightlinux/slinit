# 770-socket-listen-overhead — start + stop of a service with
# `socket-listen`, against the plain one in 710: the difference is binding
# the socket and passing its fd. It has to be `type=process` (a scripted
# service does not get the socket), and `socket-activation = immediate` so
# `start` starts it rather than waiting for a connection.
_name="perf-sock-listen-$$"
_svcfile="/etc/slinit.d/$_name"
_sock="/tmp/perf-sock-$$"
cat > "$_svcfile" <<EOS
type = process
command = /bin/sleep 3600
restart = no
socket-listen = $_sock
socket-activation = immediate
EOS
perf_run_iters "$ITERS" "CtlStartStop_socket_listen_svc" \
    "slinitctl start $_name && slinitctl stop $_name"
slinitctl unload "$_name" > /dev/null 2>&1
rm -f "$_sock" "$_svcfile"
