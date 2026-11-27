# The container is SIGKILLed (OOM killer, `docker kill`, a node losing
# power) and started again with `docker start`. Its writable layer is
# kept, so /run still holds the old control socket and state files that
# no clean shutdown removed. slinit must come up anyway, with a working
# control socket, and run the workload again.

N=slinit-ct-sigkill-$$
SVC=$(new_svcdir)
trap 'ct_rm $N; rm -rf "$SVC"' EXIT

cat > "$SVC/boot" <<'EOS'
type = internal
depends-on: app
EOS
cat > "$SVC/app" <<'EOS'
type = process
command = /bin/sh -c "while :; do sleep 1; done"
EOS

ct_start $N "$SVC"
ct_wait_ready $N 10
check $? "first boot reached STARTED"

"$RUNTIME" kill -s KILL $N >/dev/null
ct_wait_exit $N 10
st=$("$RUNTIME" diff $N 2>/dev/null | grep -c '/run/slinit.socket')
[ "$st" -ge 1 ]
check $? "the SIGKILL left the old control socket behind in /run"

for i in 1 2; do
    "$RUNTIME" start $N >/dev/null
    ct_wait_ready $N 10
    check $? "start $i after a SIGKILL: boot reached STARTED over the control socket"
    "$RUNTIME" exec $N slinitctl status app 2>/dev/null | grep -q 'State: *STARTED'
    check $? "start $i: app runs again"
    "$RUNTIME" kill -s KILL $N >/dev/null
    ct_wait_exit $N 10
done

summary
