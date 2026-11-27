# A service with `options = runs-on-console`, the usual way to give an
# interactive tool or a shell the terminal, inside a container.
#
# With `docker run -t` the runtime gives the container a pty as
# /dev/console, and the service must get it as its terminal. Without
# -t there is no /dev/console at all: the service must still start,
# with slinit's own output (what `docker logs` shows) as its stdout.

SVC=$(new_svcdir)
names=""
trap 'for n in $names; do ct_rm $n; done; rm -rf "$SVC"' EXIT

cat > "$SVC/boot" <<'EOS'
type = internal
depends-on: con
EOS
cat > "$SVC/con" <<'EOS'
type = process
command = /bin/sh -c "echo con-says-hello; tty > /tmp/contty 2>&1; while :; do sleep 1; done"
options = runs-on-console
EOS

N=slinit-ct-con-tty-$$; names="$names $N"
ct_start $N "$SVC" -t
ct_wait_ready $N 10
check $? "with a tty: boot (and the console service) reached STARTED"
t=$("$RUNTIME" exec $N cat /tmp/contty 2>/dev/null)
[ "$t" = "/dev/console" ]
check $? "with a tty: the service's terminal is /dev/console (got '$t')"
ms=$(ct_stop_ms $N 10)
ec=$(ct_exit_code $N)
[ "$ec" = "0" ] && [ "$ms" -lt 5000 ]
check $? "with a tty: docker stop exits 0 in ${ms}ms (exit $ec)"

N=slinit-ct-con-notty-$$; names="$names $N"
ct_start $N "$SVC"
ct_wait_ready $N 10
check $? "without a tty (no /dev/console): the console service still started"
t=$("$RUNTIME" exec $N cat /tmp/contty 2>/dev/null)
[ "$t" = "not a tty" ]
check $? "without a tty: the service has no terminal (got '$t')"
ct_logs $N | grep -q 'con-says-hello'
check $? "without a tty: its output reaches docker logs"
ms=$(ct_stop_ms $N 10)
ec=$(ct_exit_code $N)
[ "$ec" = "0" ] && [ "$ms" -lt 5000 ]
check $? "without a tty: docker stop exits 0 in ${ms}ms (exit $ec)"

summary
